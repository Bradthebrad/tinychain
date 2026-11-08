package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"sync"
)

// runCancellableStdio keeps input processing independent of handler execution.
// Handlers remain serialized (as in the original stdio transport). Cancellation
// is delivered to both queued and executing requests. A response acknowledges
// handler completion, never merely receipt of a cancellation notification.
func (s *Server) runCancellableStdio(ctx context.Context, in io.Reader, out io.Writer) error {
	callsCtx, cancelAll := context.WithCancel(ctx)
	defer cancelAll()
	type job struct {
		req    Request
		ctx    context.Context
		cancel context.CancelFunc
		key    string
	}
	jobs := make(chan job, 64)
	var mu sync.Mutex
	active := map[string]context.CancelFunc{}
	var writeMu sync.Mutex
	writer := bufio.NewWriter(out)
	write := func(resp Response) {
		if resp.JSONRPC == "" {
			return
		}
		writeMu.Lock()
		defer writeMu.Unlock()
		writeResponse(writer, resp)
	}
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		for j := range jobs {
			var resp Response
			if j.ctx.Err() != nil && j.req.Method == "tools/call" {
				resp = newError(j.req.ID, -32800, "request cancelled")
			} else {
				resp = s.Handle(j.ctx, j.req)
			}
			// Only the handler returning establishes that no further local mutation
			// can start. Native work already dispatched may still have uncertain outcome.
			mu.Lock()
			delete(active, j.key)
			mu.Unlock()
			j.cancel()
			write(resp)
		}
	}()
	defer func() { close(jobs); <-finished }()
	type input struct {
		line []byte
		err  error
	}
	lines := make(chan input)
	go func() {
		scanner := bufio.NewScanner(in)
		scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
		for scanner.Scan() {
			line := append([]byte(nil), scanner.Bytes()...)
			select {
			case lines <- input{line: line}:
			case <-callsCtx.Done():
				return
			}
		}
		err := scanner.Err()
		if err == nil {
			err = io.EOF
		}
		select {
		case lines <- input{err: err}:
		case <-callsCtx.Done():
		}
	}()
	for {
		select {
		case <-ctx.Done():
			cancelAll()
			return ctx.Err()
		case item := <-lines:
			if item.err != nil {
				// A disconnected client cannot approve further queued mutations.
				// Cancel them and drain the running handler before returning.
				cancelAll()
				if item.err == io.EOF {
					return nil
				}
				return item.err
			}
			var req Request
			dec := json.NewDecoder(bytes.NewReader(item.line))
			dec.UseNumber()
			if err := dec.Decode(&req); err != nil {
				write(newError(nil, -32700, "parse error"))
				continue
			}
			if req.ID == nil {
				if req.Method == "notifications/cancelled" {
					var params struct {
						RequestID any `json:"requestId"`
					}
					d := json.NewDecoder(bytes.NewReader(req.Params))
					d.UseNumber()
					if d.Decode(&params) == nil && params.RequestID != nil {
						mu.Lock()
						if cancel := active[idKey(params.RequestID)]; cancel != nil {
							cancel()
						}
						mu.Unlock()
					}
				}
				continue
			}
			callCtx, cancel := context.WithCancel(callsCtx)
			key := idKey(req.ID)
			mu.Lock()
			if _, duplicate := active[key]; duplicate {
				mu.Unlock()
				cancel()
				write(newError(req.ID, -32600, "duplicate active request id"))
				continue
			}
			active[key] = cancel
			mu.Unlock()
			select {
			case jobs <- job{req: req, ctx: callCtx, cancel: cancel, key: key}:
			default:
				mu.Lock()
				delete(active, key)
				mu.Unlock()
				cancel()
				write(newError(req.ID, -32000, "stdio request queue full"))
			}
		}
	}
}
