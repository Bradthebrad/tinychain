package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
)

type Client struct {
	transport ClientTransport
	nextID    atomic.Int64
}

type ClientTransport interface {
	RoundTrip(ctx context.Context, req Request) (Response, error)
	Close() error
}

func NewClient(transport ClientTransport) *Client {
	return &Client{transport: transport}
}

func NewHTTPClient(url string, headers map[string]string) *Client {
	return NewClient(&HTTPTransport{URL: url, Headers: headers, Client: http.DefaultClient})
}

func NewSSEClient(messageURL string, headers map[string]string) *Client {
	return NewHTTPClient(messageURL, headers)
}

func NewStdioClient(ctx context.Context, command string, args ...string) (*Client, error) {
	transport, err := NewStdioTransport(ctx, command, args...)
	if err != nil {
		return nil, err
	}
	return NewClient(transport), nil
}

func NewStdioClientWithEnv(ctx context.Context, command string, args []string, env map[string]string) (*Client, error) {
	transport, err := NewStdioTransportWithEnv(ctx, command, args, env)
	if err != nil {
		return nil, err
	}
	return NewClient(transport), nil
}

func (c *Client) Initialize(ctx context.Context) (*InitializeResult, error) {
	var result InitializeResult
	if err := c.call(ctx, "initialize", map[string]any{
		"protocolVersion": ProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      Implementation{Name: "tinychain-mcp-client"},
	}, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *Client) ListTools(ctx context.Context) ([]Tool, error) {
	var result ListToolsResult
	if err := c.call(ctx, "tools/list", nil, &result); err != nil {
		return nil, err
	}
	return result.Tools, nil
}

func (c *Client) CallTool(ctx context.Context, name string, args map[string]any) (ToolResult, error) {
	var result ToolResult
	err := c.call(ctx, "tools/call", CallToolParams{Name: name, Arguments: args}, &result)
	return result, err
}

func (c *Client) Close() error {
	if c.transport == nil {
		return nil
	}
	return c.transport.Close()
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

type HTTPTransport struct {
	URL     string
	Headers map[string]string
	Client  *http.Client
}

func (t *HTTPTransport) RoundTrip(ctx context.Context, req Request) (Response, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return Response{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, t.URL, bytes.NewReader(body))
	if err != nil {
		return Response{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range t.Headers {
		httpReq.Header.Set(k, v)
	}
	client := t.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return Response{}, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return Response{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Response{}, fmt.Errorf("mcp http: status %d: %s", resp.StatusCode, string(data))
	}
	var out Response
	if err := json.Unmarshal(data, &out); err != nil {
		return Response{}, err
	}
	return out, nil
}

func (t *HTTPTransport) Close() error {
	return nil
}

type StdioTransport struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	reader *bufio.Reader
	mu     sync.Mutex
}

func NewStdioTransport(ctx context.Context, command string, args ...string) (*StdioTransport, error) {
	return NewStdioTransportWithEnv(ctx, command, args, nil)
}

func NewStdioTransportWithEnv(ctx context.Context, command string, args []string, env map[string]string) (*StdioTransport, error) {
	cmd := exec.CommandContext(ctx, command, args...)
	if len(env) > 0 {
		cmd.Env = os.Environ()
		for key, value := range env {
			cmd.Env = append(cmd.Env, key+"="+value)
		}
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &StdioTransport{cmd: cmd, stdin: stdin, reader: bufio.NewReader(stdout)}, nil
}

func (t *StdioTransport) RoundTrip(ctx context.Context, req Request) (Response, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	data, err := json.Marshal(req)
	if err != nil {
		return Response{}, err
	}
	if _, err := t.stdin.Write(append(data, '\n')); err != nil {
		return Response{}, err
	}
	type readResult struct {
		line []byte
		err  error
	}
	done := make(chan readResult, 1)
	go func() {
		line, err := t.reader.ReadBytes('\n')
		done <- readResult{line: line, err: err}
	}()
	select {
	case <-ctx.Done():
		_ = t.Close()
		return Response{}, ctx.Err()
	case result := <-done:
		if result.err != nil {
			return Response{}, result.err
		}
		var out Response
		if err := json.Unmarshal(result.line, &out); err != nil {
			return Response{}, err
		}
		return out, nil
	}
}

func (t *StdioTransport) Close() error {
	if t.stdin != nil {
		_ = t.stdin.Close()
	}
	if t.cmd != nil && t.cmd.Process != nil {
		_ = t.cmd.Process.Kill()
		_, _ = t.cmd.Process.Wait()
	}
	return nil
}
