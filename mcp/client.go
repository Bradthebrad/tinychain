package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Client speaks JSON-RPC MCP over any ClientTransport.
type Client struct {
	transport ClientTransport
	nextID    atomic.Int64

	mu           sync.Mutex
	roots        func() []Root
	clientInfo   Implementation
	serverInfo   Implementation
	instructions string
}

// ClientTransport moves one request/response pair.
type ClientTransport interface {
	RoundTrip(ctx context.Context, req Request) (Response, error)
	Close() error
}

// Notifier is implemented by transports that can send JSON-RPC notifications
// (requests without an id and without a response).
type Notifier interface {
	Notify(ctx context.Context, req Request) error
}

// ServerRequestHandler answers requests a server sends to the client, such as
// roots/list or ping.
type ServerRequestHandler func(Request) Response

type serverRequestHandlerSetter interface {
	SetServerRequestHandler(ServerRequestHandler)
}

type protocolVersionSetter interface {
	SetProtocolVersion(string)
}

// Liveness is implemented by transports backed by a process or session that can
// end on its own.
type Liveness interface {
	Done() <-chan struct{}
	Err() error
}

// StderrSource is implemented by transports that capture a server's stderr.
type StderrSource interface {
	Stderr() string
}

func NewClient(transport ClientTransport) *Client {
	c := &Client{transport: transport, clientInfo: Implementation{Name: "tinychain-mcp-client"}}
	if setter, ok := transport.(serverRequestHandlerSetter); ok {
		setter.SetServerRequestHandler(c.handleServerRequest)
	}
	return c
}

// SetRoots registers the filesystem roots the client exposes to the server via
// roots/list. Call before Initialize so the capability is advertised.
func (c *Client) SetRoots(provider func() []Root) *Client {
	c.mu.Lock()
	c.roots = provider
	c.mu.Unlock()
	return c
}

// SetClientInfo overrides the implementation info sent during initialize.
func (c *Client) SetClientInfo(info Implementation) *Client {
	c.mu.Lock()
	c.clientInfo = info
	c.mu.Unlock()
	return c
}

func NewHTTPClient(url string, headers map[string]string) *Client {
	return NewClient(&HTTPTransport{URL: url, Headers: headers, Client: http.DefaultClient})
}

func NewSSEClient(messageURL string, headers map[string]string) *Client {
	return NewHTTPClient(messageURL, headers)
}

func NewStdioClient(ctx context.Context, command string, args ...string) (*Client, error) {
	return NewStdioClientWithOptions(ctx, StdioOptions{Command: command, Args: args})
}

func NewStdioClientWithEnv(ctx context.Context, command string, args []string, env map[string]string) (*Client, error) {
	return NewStdioClientWithOptions(ctx, StdioOptions{Command: command, Args: args, Env: env})
}

func NewStdioClientWithOptions(ctx context.Context, opts StdioOptions) (*Client, error) {
	transport, err := NewStdioTransportWithOptions(ctx, opts)
	if err != nil {
		return nil, err
	}
	return NewClient(transport), nil
}

func (c *Client) Initialize(ctx context.Context) (*InitializeResult, error) {
	c.mu.Lock()
	info := c.clientInfo
	hasRoots := c.roots != nil
	c.mu.Unlock()
	capabilities := map[string]any{}
	if hasRoots {
		capabilities["roots"] = map[string]any{"listChanged": true}
	}
	var result InitializeResult
	if err := c.call(ctx, "initialize", map[string]any{
		"protocolVersion": ProtocolVersion,
		"capabilities":    capabilities,
		"clientInfo":      info,
	}, &result); err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.serverInfo = result.ServerInfo
	c.instructions = result.Instructions
	c.mu.Unlock()
	if setter, ok := c.transport.(protocolVersionSetter); ok && result.ProtocolVersion != "" {
		setter.SetProtocolVersion(result.ProtocolVersion)
	}
	// The spec requires an initialized notification. Transports that cannot
	// send notifications (custom test transports) simply skip it.
	if notifier, ok := c.transport.(Notifier); ok {
		_ = notifier.Notify(ctx, Request{JSONRPC: JSONRPCVersion, Method: "notifications/initialized"})
	}
	return &result, nil
}

// ServerInfo returns the implementation info reported by initialize.
func (c *Client) ServerInfo() Implementation {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.serverInfo
}

// Instructions returns the optional server instructions reported by initialize.
func (c *Client) Instructions() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.instructions
}

func (c *Client) ListTools(ctx context.Context) ([]Tool, error) {
	var tools []Tool
	cursor := ""
	for page := 0; page < 50; page++ {
		var params any
		if cursor != "" {
			params = map[string]any{"cursor": cursor}
		}
		var result struct {
			Tools      []Tool `json:"tools"`
			NextCursor string `json:"nextCursor,omitempty"`
		}
		if err := c.call(ctx, "tools/list", params, &result); err != nil {
			return nil, err
		}
		tools = append(tools, result.Tools...)
		if result.NextCursor == "" || result.NextCursor == cursor {
			break
		}
		cursor = result.NextCursor
	}
	return tools, nil
}

func (c *Client) CallTool(ctx context.Context, name string, args map[string]any) (ToolResult, error) {
	var result ToolResult
	err := c.call(ctx, "tools/call", CallToolParams{Name: name, Arguments: args}, &result)
	return result, err
}

// Ping checks that the server is responsive.
func (c *Client) Ping(ctx context.Context) error {
	var out map[string]any
	return c.call(ctx, "ping", nil, &out)
}

// NotifyRootsChanged tells the server the roots list changed.
func (c *Client) NotifyRootsChanged(ctx context.Context) error {
	notifier, ok := c.transport.(Notifier)
	if !ok {
		return nil
	}
	return notifier.Notify(ctx, Request{JSONRPC: JSONRPCVersion, Method: "notifications/roots/list_changed"})
}

// Done reports when the underlying process or session ended. It returns nil
// for transports without liveness information.
func (c *Client) Done() <-chan struct{} {
	if live, ok := c.transport.(Liveness); ok {
		return live.Done()
	}
	return nil
}

// Err returns why the transport ended, if it did.
func (c *Client) Err() error {
	if live, ok := c.transport.(Liveness); ok {
		return live.Err()
	}
	return nil
}

// Stderr returns the captured tail of the server's stderr, if available.
func (c *Client) Stderr() string {
	if source, ok := c.transport.(StderrSource); ok {
		return source.Stderr()
	}
	return ""
}

func (c *Client) Close() error {
	if c.transport == nil {
		return nil
	}
	return c.transport.Close()
}

func (c *Client) handleServerRequest(req Request) Response {
	switch req.Method {
	case "ping":
		return newResponse(req.ID, map[string]any{})
	case "roots/list":
		c.mu.Lock()
		provider := c.roots
		c.mu.Unlock()
		if provider == nil {
			return newError(req.ID, -32601, "roots not supported")
		}
		roots := provider()
		if roots == nil {
			roots = []Root{}
		}
		return newResponse(req.ID, ListRootsResult{Roots: roots})
	default:
		return newError(req.ID, -32601, "method not found: "+req.Method)
	}
}

func (c *Client) call(ctx context.Context, method string, params any, out any) error {
	resp, err := c.request(ctx, method, params)
	if err != nil {
		return err
	}
	if resp.Error != nil {
		return fmt.Errorf("mcp: %s", resp.Error.Message)
	}
	data, err := json.Marshal(resp.Result)
	if err != nil {
		return err
	}
	if len(data) == 0 || string(data) == "null" {
		return nil
	}
	return json.Unmarshal(data, out)
}

func (c *Client) request(ctx context.Context, method string, params any) (Response, error) {
	id := c.nextID.Add(1)
	var raw json.RawMessage
	if params != nil {
		data, err := json.Marshal(params)
		if err != nil {
			return Response{}, err
		}
		raw = data
	}
	return c.transport.RoundTrip(ctx, Request{JSONRPC: JSONRPCVersion, ID: id, Method: method, Params: raw})
}

// wireMessage is any JSON-RPC message read from a server: a response, a
// server-to-client request, or a notification.
type wireMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

func (m wireMessage) hasID() bool {
	trimmed := bytes.TrimSpace(m.ID)
	return len(trimmed) > 0 && string(trimmed) != "null"
}

func (m wireMessage) response() Response {
	resp := Response{JSONRPC: m.JSONRPC, ID: idValue(m.ID), Error: m.Error}
	if len(m.Result) > 0 {
		resp.Result = m.Result
	}
	return resp
}

func (m wireMessage) request() Request {
	return Request{JSONRPC: m.JSONRPC, ID: idValue(m.ID), Method: m.Method, Params: m.Params}
}

func idValue(raw json.RawMessage) any {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil
	}
	return value
}

func idKey(id any) string {
	switch typed := id.(type) {
	case json.RawMessage:
		return normalizeIDKey(string(bytes.TrimSpace(typed)))
	case nil:
		return ""
	default:
		data, _ := json.Marshal(typed)
		return normalizeIDKey(string(data))
	}
}

// normalizeIDKey makes 1, 1.0 and "1"-as-number compare equal while keeping
// string ids distinct from numbers.
func normalizeIDKey(raw string) string {
	if raw == "" {
		return ""
	}
	if raw[0] == '"' {
		return raw
	}
	var number json.Number
	if err := json.Unmarshal([]byte(raw), &number); err == nil {
		if i, err := number.Int64(); err == nil {
			return fmt.Sprintf("%d", i)
		}
		return number.String()
	}
	return raw
}

// ---------------------------------------------------------------------------
// HTTP (streamable HTTP) transport

type HTTPTransport struct {
	URL     string
	Headers map[string]string
	Client  *http.Client

	mu              sync.Mutex
	sessionID       string
	protocolVersion string
	handler         ServerRequestHandler
}

func (t *HTTPTransport) SetServerRequestHandler(handler ServerRequestHandler) {
	t.mu.Lock()
	t.handler = handler
	t.mu.Unlock()
}

func (t *HTTPTransport) SetProtocolVersion(version string) {
	t.mu.Lock()
	t.protocolVersion = version
	t.mu.Unlock()
}

// SessionID returns the Mcp-Session-Id assigned by the server, if any.
func (t *HTTPTransport) SessionID() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.sessionID
}

func (t *HTTPTransport) client() *http.Client {
	if t.Client != nil {
		return t.Client
	}
	return http.DefaultClient
}

func (t *HTTPTransport) newRequest(ctx context.Context, method string, body []byte) (*http.Request, error) {
	httpReq, err := http.NewRequestWithContext(ctx, method, t.URL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json, text/event-stream")
	t.mu.Lock()
	if t.sessionID != "" {
		httpReq.Header.Set("Mcp-Session-Id", t.sessionID)
	}
	if t.protocolVersion != "" {
		httpReq.Header.Set("MCP-Protocol-Version", t.protocolVersion)
	}
	t.mu.Unlock()
	for k, v := range t.Headers {
		httpReq.Header.Set(k, v)
	}
	return httpReq, nil
}

func (t *HTTPTransport) RoundTrip(ctx context.Context, req Request) (Response, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return Response{}, err
	}
	httpReq, err := t.newRequest(ctx, http.MethodPost, body)
	if err != nil {
		return Response{}, err
	}
	resp, err := t.client().Do(httpReq)
	if err != nil {
		return Response{}, err
	}
	defer resp.Body.Close()
	if session := resp.Header.Get("Mcp-Session-Id"); session != "" {
		t.mu.Lock()
		t.sessionID = session
		t.mu.Unlock()
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return Response{}, fmt.Errorf("mcp http: status %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	want := idKey(req.ID)
	if strings.HasPrefix(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		return t.readEventStream(ctx, resp.Body, want)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return Response{}, err
	}
	var msg wireMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		return Response{}, err
	}
	return msg.response(), nil
}

// readEventStream reads SSE events until the response matching want arrives.
// Server requests carried on the stream are answered with a separate POST.
func (t *HTTPTransport) readEventStream(ctx context.Context, body io.Reader, want string) (Response, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	var data strings.Builder
	flush := func() (Response, bool) {
		payload := strings.TrimSpace(data.String())
		data.Reset()
		if payload == "" {
			return Response{}, false
		}
		var msg wireMessage
		if err := json.Unmarshal([]byte(payload), &msg); err != nil {
			return Response{}, false
		}
		if msg.Method != "" {
			if msg.hasID() {
				go t.answer(msg.request())
			}
			return Response{}, false
		}
		if idKey(msg.ID) == want {
			return msg.response(), true
		}
		return Response{}, false
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if resp, ok := flush(); ok {
				return resp, nil
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if resp, ok := flush(); ok {
		return resp, nil
	}
	if err := ctx.Err(); err != nil {
		return Response{}, err
	}
	if err := scanner.Err(); err != nil {
		return Response{}, err
	}
	return Response{}, errors.New("mcp http: event stream ended without a response")
}

func (t *HTTPTransport) answer(req Request) {
	t.mu.Lock()
	handler := t.handler
	t.mu.Unlock()
	if handler == nil {
		return
	}
	resp := handler(req)
	body, err := json.Marshal(resp)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	httpReq, err := t.newRequest(ctx, http.MethodPost, body)
	if err != nil {
		return
	}
	if r, err := t.client().Do(httpReq); err == nil {
		_, _ = io.Copy(io.Discard, r.Body)
		r.Body.Close()
	}
}

func (t *HTTPTransport) Notify(ctx context.Context, req Request) error {
	req.ID = nil
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	httpReq, err := t.newRequest(ctx, http.MethodPost, body)
	if err != nil {
		return err
	}
	resp, err := t.client().Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("mcp http: notify status %d", resp.StatusCode)
	}
	return nil
}

// Close ends the session on servers that issued one.
func (t *HTTPTransport) Close() error {
	session := t.SessionID()
	if session == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	httpReq, err := t.newRequest(ctx, http.MethodDelete, nil)
	if err != nil {
		return nil
	}
	if resp, err := t.client().Do(httpReq); err == nil {
		resp.Body.Close()
	}
	t.mu.Lock()
	t.sessionID = ""
	t.mu.Unlock()
	return nil
}

// ---------------------------------------------------------------------------
// stdio transport

// StdioOptions configures a stdio MCP server process.
type StdioOptions struct {
	Command string
	Args    []string
	// Env entries are appended to the current environment.
	Env map[string]string
	// Dir is the working directory for the server process. Empty inherits.
	Dir string
	// StderrBytes is how much stderr tail to keep (default 32 KiB).
	StderrBytes int
}

// StdioTransport multiplexes concurrent requests over a server's stdin/stdout,
// routes responses by id, answers server-to-client requests, and ignores
// notifications.
type StdioTransport struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	reader *bufio.Reader
	stderr *tailBuffer
	tree   *processTree

	writeMu sync.Mutex

	mu       sync.Mutex
	pending  map[string]chan Response
	early    map[string]Response // responses that arrived before their waiter registered
	handler  ServerRequestHandler
	started  bool
	done     chan struct{}
	err      error
	waitOnce sync.Once
	closed   bool
}

func NewStdioTransport(ctx context.Context, command string, args ...string) (*StdioTransport, error) {
	return NewStdioTransportWithOptions(ctx, StdioOptions{Command: command, Args: args})
}

func NewStdioTransportWithEnv(ctx context.Context, command string, args []string, env map[string]string) (*StdioTransport, error) {
	return NewStdioTransportWithOptions(ctx, StdioOptions{Command: command, Args: args, Env: env})
}

func NewStdioTransportWithOptions(ctx context.Context, opts StdioOptions) (*StdioTransport, error) {
	if strings.TrimSpace(opts.Command) == "" {
		return nil, errors.New("mcp: stdio command is required")
	}
	cmd := exec.CommandContext(ctx, opts.Command, opts.Args...)
	configureStdioCommand(cmd)
	if opts.Dir != "" {
		cmd.Dir = opts.Dir
	}
	if len(opts.Env) > 0 {
		cmd.Env = os.Environ()
		for key, value := range opts.Env {
			cmd.Env = append(cmd.Env, key+"="+value)
		}
	}
	limit := opts.StderrBytes
	if limit <= 0 {
		limit = 32 * 1024
	}
	stderr := &tailBuffer{limit: limit}
	cmd.Stderr = stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	t := &StdioTransport{cmd: cmd, stdin: stdin, reader: bufio.NewReaderSize(stdout, 64*1024), stderr: stderr}
	cmd.Cancel = func() error {
		t.tree.kill(cmd)
		return nil
	}
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	t.tree = attachProcessTree(cmd)
	t.start()
	return t, nil
}

func (t *StdioTransport) SetServerRequestHandler(handler ServerRequestHandler) {
	t.mu.Lock()
	t.handler = handler
	t.mu.Unlock()
}

// Stderr returns the captured tail of the server's stderr.
func (t *StdioTransport) Stderr() string {
	if t.stderr == nil {
		return ""
	}
	return t.stderr.String()
}

// Pid returns the server process id, or 0.
func (t *StdioTransport) Pid() int {
	if t.cmd == nil || t.cmd.Process == nil {
		return 0
	}
	return t.cmd.Process.Pid
}

func (t *StdioTransport) Done() <-chan struct{} {
	t.start()
	return t.done
}

func (t *StdioTransport) Err() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.err
}

func (t *StdioTransport) start() {
	t.mu.Lock()
	if t.started {
		t.mu.Unlock()
		return
	}
	t.started = true
	t.pending = map[string]chan Response{}
	t.early = map[string]Response{}
	t.done = make(chan struct{})
	t.mu.Unlock()
	go t.readLoop()
}

func (t *StdioTransport) readLoop() {
	var readErr error
	for {
		line, err := t.reader.ReadBytes('\n')
		if trimmed := bytes.TrimSpace(line); len(trimmed) > 0 {
			t.dispatch(trimmed)
		}
		if err != nil {
			readErr = err
			break
		}
	}
	t.wait()
	t.mu.Lock()
	if t.err == nil {
		if t.closed {
			t.err = errors.New("mcp: transport closed")
		} else {
			msg := "mcp: server exited"
			if readErr != nil && readErr != io.EOF {
				msg += ": " + readErr.Error()
			}
			if tail := strings.TrimSpace(t.Stderr()); tail != "" {
				msg += "\n" + lastLines(tail, 8)
			}
			t.err = errors.New(msg)
		}
	}
	t.pending = map[string]chan Response{}
	close(t.done)
	t.mu.Unlock()
}

func (t *StdioTransport) dispatch(line []byte) {
	var msg wireMessage
	if err := json.Unmarshal(line, &msg); err != nil {
		return // Ignore non-JSON output such as stray logging.
	}
	if msg.Method != "" {
		if msg.hasID() {
			t.mu.Lock()
			handler := t.handler
			t.mu.Unlock()
			req := msg.request()
			resp := newError(req.ID, -32601, "method not found: "+req.Method)
			if handler != nil {
				resp = handler(req)
			}
			go func() { _ = t.writeMessage(resp) }()
		}
		return
	}
	key := normalizeIDKey(string(bytes.TrimSpace(msg.ID)))
	t.mu.Lock()
	ch := t.pending[key]
	delete(t.pending, key)
	if ch == nil {
		if len(t.early) >= 64 {
			for stale := range t.early {
				delete(t.early, stale)
				break
			}
		}
		t.early[key] = msg.response()
	}
	t.mu.Unlock()
	if ch != nil {
		ch <- msg.response()
	}
}

func (t *StdioTransport) writeMessage(message any) error {
	data, err := json.Marshal(message)
	if err != nil {
		return err
	}
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	_, err = t.stdin.Write(append(data, '\n'))
	return err
}

func (t *StdioTransport) RoundTrip(ctx context.Context, req Request) (Response, error) {
	t.start()
	key := idKey(req.ID)
	ch := make(chan Response, 1)
	t.mu.Lock()
	select {
	case <-t.done:
		err := t.err
		t.mu.Unlock()
		if err == nil {
			err = errors.New("mcp: transport closed")
		}
		return Response{}, err
	default:
	}
	if resp, ok := t.early[key]; ok {
		delete(t.early, key)
		t.mu.Unlock()
		return resp, nil
	}
	t.pending[key] = ch
	t.mu.Unlock()
	if err := t.writeMessage(req); err != nil {
		t.forget(key)
		return Response{}, err
	}
	select {
	case <-ctx.Done():
		t.forget(key)
		return Response{}, ctx.Err()
	case resp := <-ch:
		return resp, nil
	case <-t.done:
		select {
		case resp := <-ch:
			return resp, nil
		default:
		}
		return Response{}, t.Err()
	}
}

func (t *StdioTransport) Notify(ctx context.Context, req Request) error {
	req.ID = nil
	return t.writeMessage(req)
}

func (t *StdioTransport) forget(key string) {
	t.mu.Lock()
	delete(t.pending, key)
	t.mu.Unlock()
}

func (t *StdioTransport) wait() {
	t.waitOnce.Do(func() {
		if t.cmd != nil && t.cmd.Process != nil {
			_ = t.cmd.Wait()
		}
		t.tree.release()
	})
}

// Close stops the server and every process it started.
func (t *StdioTransport) Close() error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	started := t.started
	done := t.done
	t.mu.Unlock()
	if t.stdin != nil {
		_ = t.stdin.Close()
	}
	if t.cmd != nil && t.cmd.Process != nil {
		t.tree.kill(t.cmd)
	}
	if started && done != nil && t.cmd != nil {
		select {
		case <-done:
		case <-time.After(3 * time.Second):
		}
	} else {
		t.wait()
	}
	return nil
}

// tailBuffer keeps the last limit bytes written to it.
type tailBuffer struct {
	mu    sync.Mutex
	limit int
	buf   []byte
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	if over := len(b.buf) - b.limit; over > 0 {
		b.buf = append([]byte(nil), b.buf[over:]...)
	}
	return len(p), nil
}

func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}

func lastLines(text string, n int) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
