package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// helperMain lets tests re-exec the test binary as a scripted MCP server.
func TestMain(m *testing.M) {
	switch os.Getenv("TINYCHAIN_MCP_HELPER") {
	case "roots":
		helperRootsServer()
	case "cwd":
		helperCwdServer()
	case "spawn":
		helperSpawnServer()
	case "sleeper":
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func helperClient(t *testing.T, mode string, dir string) *Client {
	t.Helper()
	client, err := NewStdioClientWithOptions(context.Background(), StdioOptions{
		Command: os.Args[0],
		Args:    []string{"-test.run=^$"},
		Env:     map[string]string{"TINYCHAIN_MCP_HELPER": mode},
		Dir:     dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func writeLine(w *bufio.Writer, v any) {
	data, _ := json.Marshal(v)
	_, _ = w.Write(append(data, '\n'))
	_ = w.Flush()
}

// helperRootsServer answers initialize, then on tools/list first sends a
// notification and a roots/list request to the client, and replies with the
// roots it received. A second tools/list (id 3) is answered before id 2 to
// exercise out-of-order routing.
func helperRootsServer() {
	in := bufio.NewReader(os.Stdin)
	out := bufio.NewWriter(os.Stdout)
	var pendingList []Request
	for {
		line, err := in.ReadBytes('\n')
		if err != nil {
			os.Exit(0)
		}
		var msg wireMessage
		if json.Unmarshal(line, &msg) != nil {
			continue
		}
		switch {
		case msg.Method == "initialize":
			writeLine(out, newResponse(idValue(msg.ID), InitializeResult{ProtocolVersion: ProtocolVersion, ServerInfo: Implementation{Name: "roots-helper"}, Instructions: "be nice"}))
		case msg.Method == "notifications/initialized":
			fmt.Fprintln(os.Stderr, "got initialized")
		case msg.Method == "tools/list":
			pendingList = append(pendingList, msg.request())
			if len(pendingList) == 1 {
				writeLine(out, map[string]any{"jsonrpc": "2.0", "method": "notifications/message", "params": map[string]any{"level": "info"}})
				writeLine(out, map[string]any{"jsonrpc": "2.0", "id": "srv-1", "method": "roots/list"})
			}
		case msg.Method == "" && string(msg.ID) == `"srv-1"`:
			var roots ListRootsResult
			_ = json.Unmarshal(msg.Result, &roots)
			names := []string{}
			for _, root := range roots.Roots {
				names = append(names, root.URI)
			}
			// Answer in reverse order of arrival once both lists are pending.
			for len(pendingList) < 2 {
				line, err := in.ReadBytes('\n')
				if err != nil {
					os.Exit(0)
				}
				var next wireMessage
				if json.Unmarshal(line, &next) == nil && next.Method == "tools/list" {
					pendingList = append(pendingList, next.request())
				}
			}
			for i := len(pendingList) - 1; i >= 0; i-- {
				writeLine(out, newResponse(pendingList[i].ID, ListToolsResult{Tools: []Tool{{Name: "roots:" + strings.Join(names, ","), Title: "Roots " + strconv.Itoa(i)}}}))
			}
			pendingList = nil
		}
	}
}

func helperCwdServer() {
	in := bufio.NewReader(os.Stdin)
	out := bufio.NewWriter(os.Stdout)
	line, _ := in.ReadBytes('\n')
	var msg wireMessage
	_ = json.Unmarshal(line, &msg)
	cwd, _ := os.Getwd()
	writeLine(out, newResponse(idValue(msg.ID), InitializeResult{ProtocolVersion: ProtocolVersion, ServerInfo: Implementation{Name: cwd}}))
	_, _ = in.ReadBytes('\n')
	time.Sleep(time.Minute)
}

// helperSpawnServer starts a long-lived grandchild and reports its pid.
func helperSpawnServer() {
	in := bufio.NewReader(os.Stdin)
	out := bufio.NewWriter(os.Stdout)
	child := exec.Command(os.Args[0], "-test.run=^$")
	child.Env = append(os.Environ(), "TINYCHAIN_MCP_HELPER=sleeper")
	_ = child.Start()
	line, _ := in.ReadBytes('\n')
	var msg wireMessage
	_ = json.Unmarshal(line, &msg)
	writeLine(out, newResponse(idValue(msg.ID), InitializeResult{ProtocolVersion: ProtocolVersion, ServerInfo: Implementation{Name: strconv.Itoa(child.Process.Pid)}}))
	time.Sleep(time.Minute)
}

func TestStdioAnswersRootsAndRoutesOutOfOrderResponses(t *testing.T) {
	client := helperClient(t, "roots", "")
	client.SetRoots(func() []Root { return []Root{{URI: "file:///work/a", Name: "a"}, {URI: "file:///work/b"}} })
	result, err := client.Initialize(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.ServerInfo.Name != "roots-helper" || client.Instructions() != "be nice" {
		t.Fatalf("initialize = %#v", result)
	}
	var wg sync.WaitGroup
	got := make([][]Tool, 2)
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			got[i], errs[i] = client.ListTools(ctx)
		}(i)
		time.Sleep(50 * time.Millisecond)
	}
	wg.Wait()
	for i := range got {
		if errs[i] != nil {
			t.Fatalf("list %d: %v (stderr %q)", i, errs[i], client.Stderr())
		}
		if len(got[i]) != 1 || got[i][0].Name != "roots:file:///work/a,file:///work/b" {
			t.Fatalf("list %d = %#v", i, got[i])
		}
	}
	if !strings.Contains(client.Stderr(), "got initialized") {
		t.Fatalf("initialized notification not sent; stderr=%q", client.Stderr())
	}
}

func TestStdioOptionsDirSetsServerWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	client := helperClient(t, "cwd", dir)
	result, err := client.Initialize(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.EvalSymlinks(dir)
	gotDir, _ := filepath.EvalSymlinks(result.ServerInfo.Name)
	if gotDir != want {
		t.Fatalf("server cwd = %q, want %q", gotDir, want)
	}
}

func TestStdioCloseKillsProcessTree(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process inspection below is unix-only; job objects are covered on Windows builds")
	}
	client := helperClient(t, "spawn", "")
	result, err := client.Initialize(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(result.ServerInfo.Name)
	if pid <= 0 {
		t.Fatalf("bad grandchild pid %q", result.ServerInfo.Name)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("grandchild %d still alive after Close", pid)
}

func processAlive(pid int) bool {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	fields := strings.Fields(string(data))
	return len(fields) > 2 && fields[2] != "Z"
}

func TestStdioReportsServerExitWithStderr(t *testing.T) {
	client, err := NewStdioClientWithOptions(context.Background(), StdioOptions{Command: os.Args[0], Args: []string{"-test.run=^$"}, Env: map[string]string{"TINYCHAIN_MCP_HELPER": "none-exit"}})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	select {
	case <-client.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("expected server exit to be observed")
	}
	if client.Err() == nil || !strings.Contains(client.Err().Error(), "server exited") {
		t.Fatalf("err = %v", client.Err())
	}
	if _, err := client.ListTools(context.Background()); err == nil {
		t.Fatal("expected error after exit")
	}
}

func TestHTTPTransportHandlesSessionAndEventStream(t *testing.T) {
	var mu sync.Mutex
	var sawSession, sawVersion, sawInitialized, sawDelete bool
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Method == http.MethodDelete {
			sawDelete = r.Header.Get("Mcp-Session-Id") == "sess-42"
			return
		}
		var req Request
		_ = json.NewDecoder(r.Body).Decode(&req)
		switch req.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "sess-42")
			_ = json.NewEncoder(w).Encode(newResponse(req.ID, InitializeResult{ProtocolVersion: "2025-06-18", ServerInfo: Implementation{Name: "remote"}}))
		case "notifications/initialized":
			sawInitialized = req.ID == nil
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			sawSession = r.Header.Get("Mcp-Session-Id") == "sess-42"
			sawVersion = r.Header.Get("MCP-Protocol-Version") == "2025-06-18"
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\"}\n\n")
			payload, _ := json.Marshal(newResponse(req.ID, ListToolsResult{Tools: []Tool{{Name: "remote_search", Annotations: &ToolAnnotations{ReadOnlyHint: Bool(true)}}}}))
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", payload)
		}
	})
	server := httptest.NewServer(handler)
	defer server.Close()
	client := NewHTTPClient(server.URL, nil)
	if _, err := client.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	tools, err := client.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 || tools[0].Name != "remote_search" || tools[0].Annotations == nil || !*tools[0].Annotations.ReadOnlyHint {
		t.Fatalf("tools = %#v", tools)
	}
	_ = client.Close()
	mu.Lock()
	defer mu.Unlock()
	if !sawSession || !sawVersion || !sawInitialized || !sawDelete {
		t.Fatalf("session=%v version=%v initialized=%v delete=%v", sawSession, sawVersion, sawInitialized, sawDelete)
	}
}

func TestRemoteToolReportsInfoMetaAndErrors(t *testing.T) {
	server := NewServer("adapter")
	server.AddTool(Tool{Name: "list_things", Title: "List things", Annotations: &ToolAnnotations{ReadOnlyHint: Bool(true)}, Handler: func(ctx context.Context, args map[string]any) (ToolResult, error) {
		return ToolResult{Content: []Content{{Type: "text", Text: "two things"}, {Type: "image", MimeType: "image/png", Data: "aGVsbG8="}}, StructuredContent: map[string]any{"things": []any{"a", "b"}}}, nil
	}})
	server.AddTool(Tool{Name: "explode", Handler: func(ctx context.Context, args map[string]any) (ToolResult, error) {
		return ToolResult{Content: []Content{{Type: "text", Text: "boom"}}, IsError: true}, nil
	}})
	httpServer := httptest.NewServer(server.Handler("/mcp"))
	defer httpServer.Close()
	client := NewHTTPClient(httpServer.URL+"/mcp", nil)
	tools, err := AgentToolsForServer(context.Background(), client, "things-server", "Things")
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]*RemoteTool{}
	for _, tool := range tools {
		byName[tool.Definition().Name] = tool.(*RemoteTool)
	}
	list := byName["list_things"]
	info := list.ToolInfo()
	if info.Title != "List things" || info.Server != "things-server" || info.ReadOnly == nil || !*info.ReadOnly {
		t.Fatalf("info = %#v", info)
	}
	text, meta, err := list.CallWithMeta(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "two things") || !strings.Contains(text, "[image image/png") {
		t.Fatalf("text = %q", text)
	}
	if meta["items"] != 2 || meta["structured"] != true {
		t.Fatalf("meta = %#v", meta)
	}
	if _, _, err := byName["explode"].CallWithMeta(context.Background(), nil); err == nil || err.Error() != "boom" {
		t.Fatalf("explode err = %v", err)
	}
}
