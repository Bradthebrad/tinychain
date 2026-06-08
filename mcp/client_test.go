package mcp

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"testing"
	"time"
)

type recordingTransport struct {
	methods []string
}

func (t *recordingTransport) RoundTrip(ctx context.Context, req Request) (Response, error) {
	t.methods = append(t.methods, req.Method)
	if req.Method != "initialize" {
		return Response{}, errors.New("unexpected request: " + req.Method)
	}
	return Response{JSONRPC: JSONRPCVersion, ID: req.ID, Result: InitializeResult{
		ProtocolVersion: ProtocolVersion,
		Capabilities:    ServerCapabilities{Tools: map[string]any{}},
		ServerInfo:      Implementation{Name: "test"},
	}}, nil
}

func (t *recordingTransport) Close() error { return nil }

func TestInitializeDoesNotWaitForInitializedNotification(t *testing.T) {
	transport := &recordingTransport{}
	client := NewClient(transport)
	if _, err := client.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(transport.methods) != 1 || transport.methods[0] != "initialize" {
		t.Fatalf("methods = %#v", transport.methods)
	}
}

func TestStdioClientPassesEnvironment(t *testing.T) {
	if os.Getenv("TINYCHAIN_MCP_ENV_HELPER") == "1" {
		helperEnvServer()
		return
	}
	client, err := NewStdioClientWithEnv(context.Background(), os.Args[0], []string{"-test.run=TestStdioClientPassesEnvironment", "--"}, map[string]string{
		"TINYCHAIN_MCP_ENV_HELPER": "1",
		"TINYCHAIN_MCP_ENV_VALUE":  "hello-env",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	result, err := client.Initialize(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.ServerInfo.Name != "hello-env" {
		t.Fatalf("server info = %#v", result.ServerInfo)
	}
}

func helperEnvServer() {
	reader := bufio.NewReader(os.Stdin)
	_, _ = reader.ReadBytes('\n')
	fmt.Printf(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"%s","capabilities":{},"serverInfo":{"name":"%s"}}}`+"\n", ProtocolVersion, os.Getenv("TINYCHAIN_MCP_ENV_VALUE"))
	os.Exit(0)
}

type closeWriter struct {
	io.Writer
	close func() error
}

func (w closeWriter) Close() error {
	if w.close == nil {
		return nil
	}
	return w.close()
}

func TestStdioRoundTripRespectsContextWhileWaitingForResponse(t *testing.T) {
	reader, writer := io.Pipe()
	defer reader.Close()
	transport := &StdioTransport{
		stdin:  closeWriter{Writer: io.Discard, close: writer.Close},
		reader: bufio.NewReader(reader),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := transport.RoundTrip(ctx, Request{JSONRPC: JSONRPCVersion, ID: 1, Method: "tools/list"}); err == nil {
		t.Fatal("expected context timeout")
	}
}

func TestStdioRoundTripSerializesConcurrentRequests(t *testing.T) {
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	transport := &StdioTransport{
		stdin:  closeWriter{Writer: io.Discard},
		reader: bufio.NewReader(reader),
	}
	go func() {
		_, _ = writer.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}` + "\n"))
		_, _ = writer.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{}}` + "\n"))
	}()
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(id int64) {
			defer wg.Done()
			if _, err := transport.RoundTrip(context.Background(), Request{JSONRPC: JSONRPCVersion, ID: id, Method: "ping"}); err != nil {
				t.Errorf("round trip %d: %v", id, err)
			}
		}(int64(i + 1))
	}
	wg.Wait()
}
