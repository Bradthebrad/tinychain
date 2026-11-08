package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func stdioPair(t *testing.T, s *Server) *StdioTransport {
	t.Helper()
	in, send := io.Pipe()
	receive, out := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.RunStdio(ctx, in, out); out.Close() }()
	transport := &StdioTransport{stdin: send, reader: bufio.NewReader(receive)}
	t.Cleanup(func() {
		cancel()
		send.Close()
		receive.Close()
		in.Close()
		out.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("server did not drain")
		}
	})
	return transport
}

func toolRequest(id any, name string) Request {
	p, _ := json.Marshal(CallToolParams{Name: name})
	return Request{JSONRPC: JSONRPCVersion, ID: id, Method: "tools/call", Params: p}
}

func awaitSignal(t *testing.T, c <-chan struct{}) {
	t.Helper()
	select {
	case <-c:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for transport event")
	}
}

func TestStdioCancellationDrainsDispatchedHandler(t *testing.T) {
	for _, during := range []bool{false, true} {
		name := "before_mutation"
		if during {
			name = "during_native_mutation"
		}
		t.Run(name, func(t *testing.T) {
			s := NewServer("cancellation")
			entered, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var mutations atomic.Int32
			s.AddTool(Tool{Name: "mutate", Handler: func(ctx context.Context, _ map[string]any) (ToolResult, error) {
				if during {
					mutations.Add(1)
				}
				close(entered)
				<-ctx.Done()
				close(cancelled)
				<-release // simulate a native operation whose return cannot be interrupted
				if !during && ctx.Err() == nil {
					mutations.Add(1)
				}
				return Text("completed"), nil
			}})
			tr := stdioPair(t, s)
			ctx, cancel := context.WithCancel(WithCancellationDrain(context.Background()))
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := tr.RoundTrip(ctx, toolRequest(1, "mutate")); done <- err }()
			awaitSignal(t, entered)
			cancel()
			awaitSignal(t, cancelled)
			select {
			case err := <-done:
				t.Fatalf("returned before handler completion: %v", err)
			case <-time.After(20 * time.Millisecond):
			}
			close(release)
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("not acknowledged")
			}
			want := int32(0)
			if during {
				want = 1
			}
			if mutations.Load() != want {
				t.Fatalf("mutations=%d", mutations.Load())
			}
		})
	}
}

func TestStdioCancellationQueuedRequestNeverDispatched(t *testing.T) {
	s := NewServer("queue")
	entered := make(chan struct{})
	var calls atomic.Int32
	s.AddTool(Tool{Name: "block", Handler: func(ctx context.Context, _ map[string]any) (ToolResult, error) {
		close(entered)
		<-ctx.Done()
		return Text("done"), nil
	}})
	s.AddTool(Tool{Name: "mutate", Handler: func(context.Context, map[string]any) (ToolResult, error) { calls.Add(1); return Text("bad"), nil }})
	tr := stdioPair(t, s)
	first := make(chan error, 1)
	go func() { _, err := tr.RoundTrip(context.Background(), toolRequest("first", "block")); first <- err }()
	awaitSignal(t, entered)
	// Write the queued request synchronously, followed by its cancellation.
	// Then cancel the running handler. FIFO input processing proves cancellation
	// was recorded before the worker is able to dequeue the mutation.
	if err := tr.writeMessage(toolRequest("queued", "mutate")); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"queued", "first"} {
		p, _ := json.Marshal(map[string]any{"requestId": id})
		if err := tr.Notify(context.Background(), Request{JSONRPC: JSONRPCVersion, Method: "notifications/cancelled", Params: p}); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case err := <-first:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first not completed")
	}
	// A ping is queued after the cancelled call and therefore proves the queue drained.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := tr.RoundTrip(ctx, Request{JSONRPC: JSONRPCVersion, ID: "barrier", Method: "ping"}); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatal("cancelled queued mutation dispatched")
	}
}

func TestStdioCancellationBeforeWrite(t *testing.T) {
	for _, queued := range []bool{false, true} {
		t.Run(map[bool]string{false: "already_cancelled", true: "write_lock_queue"}[queued], func(t *testing.T) {
			receive, out := io.Pipe()
			defer receive.Close()
			defer out.Close()
			var sent bytes.Buffer
			tr := &StdioTransport{stdin: closeWriter{Writer: &sent}, reader: bufio.NewReader(receive)}
			ctx, cancel := context.WithCancel(context.Background())
			if queued {
				tr.writeMu.Lock()
			} else {
				cancel()
			}
			done := make(chan error, 1)
			go func() { _, err := tr.RoundTrip(ctx, toolRequest(1, "mutate")); done <- err }()
			if queued {
				// Wait until registration proves RoundTrip has passed its initial check.
				deadline := time.Now().Add(time.Second)
				for {
					tr.mu.Lock()
					n := len(tr.pending)
					tr.mu.Unlock()
					if n == 1 {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("no pending call")
					}
					time.Sleep(time.Millisecond)
				}
				cancel()
				tr.writeMu.Unlock()
			}
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("request stuck")
			}
			if sent.Len() != 0 {
				t.Fatalf("cancelled request written: %s", sent.String())
			}
		})
	}
}

// This regression also crosses real subprocess stdin/stdout, not only pipes
// within the test process. Markers contain no tool arguments or secret values.
func TestStdioCancellationSubprocess(t *testing.T) {
	if os.Getenv("TINYCHAIN_CANCEL_HELPER") == "1" {
		s := NewServer("cancel-helper")
		s.AddTool(Tool{Name: "mutate", Handler: func(ctx context.Context, _ map[string]any) (ToolResult, error) {
			fmt.Fprintln(os.Stderr, "entered")
			<-ctx.Done()
			fmt.Fprintln(os.Stderr, "cancelled")
			time.Sleep(50 * time.Millisecond)
			fmt.Fprintln(os.Stderr, "drained")
			return Text("uncertain"), nil
		}})
		_ = s.RunStdio(context.Background(), os.Stdin, os.Stdout)
		os.Exit(0)
	}
	tr, err := NewStdioTransportWithOptions(context.Background(), StdioOptions{Command: os.Args[0], Args: []string{"-test.run=^TestStdioCancellationSubprocess$"}, Env: map[string]string{"TINYCHAIN_CANCEL_HELPER": "1"}})
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	ctx, cancel := context.WithCancel(WithCancellationDrain(context.Background()))
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := tr.RoundTrip(ctx, toolRequest(1, "mutate")); done <- err }()
	deadline := time.Now().Add(3 * time.Second)
	for !strings.Contains(tr.Stderr(), "entered") {
		if time.Now().After(deadline) {
			t.Fatal("helper not entered")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("helper cancellation not drained")
	}
	if !strings.Contains(tr.Stderr(), "cancelled") || !strings.Contains(tr.Stderr(), "drained") {
		t.Fatal("returned without completion acknowledgment")
	}
}

func TestStdioDisconnectCancelsAndDrains(t *testing.T) {
	s := NewServer("disconnect")
	entered, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	s.AddTool(Tool{Name: "block", Handler: func(ctx context.Context, _ map[string]any) (ToolResult, error) {
		close(entered)
		<-ctx.Done()
		close(cancelled)
		<-release
		return Text("drained"), nil
	}})
	in, send := io.Pipe()
	defer in.Close()
	done := make(chan error, 1)
	go func() { done <- s.RunStdio(context.Background(), in, io.Discard) }()
	if err := json.NewEncoder(send).Encode(toolRequest(1, "block")); err != nil {
		t.Fatal(err)
	}
	awaitSignal(t, entered)
	send.Close()
	awaitSignal(t, cancelled)
	select {
	case <-done:
		t.Fatal("server returned without draining")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("server did not drain")
	}
}
