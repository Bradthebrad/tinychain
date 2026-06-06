package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

func (s *Server) Run(ctx context.Context, opts ...Option) error {
	config := options{Transport: "stdio", Addr: ":8080", Path: "/mcp", SSEPath: "/sse", MessagePath: "/message"}
	for _, opt := range opts {
		opt(&config)
	}
	switch config.Transport {
	case "", "stdio":
		return s.RunStdio(ctx, config.Stdin, config.Stdout)
	case "http", "streamable-http", "streamable_http":
		return s.RunHTTP(ctx, config.Addr, config.Path)
	case "sse":
		return s.RunSSE(ctx, config.Addr, config.SSEPath, config.MessagePath)
	default:
		return fmt.Errorf("unknown transport %q", config.Transport)
	}
}

type Option func(*options)

type options struct {
	Transport   string
	Addr        string
	Path        string
	SSEPath     string
	MessagePath string
	Stdin       io.Reader
	Stdout      io.Writer
}

func WithTransport(transport string) Option {
	return func(o *options) { o.Transport = transport }
}

func WithAddr(addr string) Option {
	return func(o *options) { o.Addr = addr }
}

func WithPath(path string) Option {
	return func(o *options) { o.Path = path }
}

func WithSSEPaths(ssePath, messagePath string) Option {
	return func(o *options) {
		o.SSEPath = ssePath
		o.MessagePath = messagePath
	}
}

func WithStdio(stdin io.Reader, stdout io.Writer) Option {
	return func(o *options) {
		o.Stdin = stdin
		o.Stdout = stdout
	}
}

func (s *Server) RunStdio(ctx context.Context, stdin io.Reader, stdout io.Writer) error {
	if stdin == nil {
		stdin = defaultStdin()
	}
	if stdout == nil {
		stdout = defaultStdout()
	}
	scanner := bufio.NewScanner(stdin)
	writer := bufio.NewWriter(stdout)
	defer writer.Flush()
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		var req Request
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			writeResponse(writer, newError(nil, -32700, err.Error()))
			continue
		}
		resp := s.Handle(ctx, req)
		if resp.JSONRPC == "" {
			continue
		}
		writeResponse(writer, resp)
	}
	return scanner.Err()
}

func (s *Server) Handler(path string) http.Handler {
	if path == "" {
		path = "/mcp"
	}
	mux := http.NewServeMux()
	mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			_, _ = io.WriteString(w, ": tinychain mcp stream\n\n")
		case http.MethodPost:
			s.serveJSONRPC(w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	return mux
}

func (s *Server) RunHTTP(ctx context.Context, addr, path string) error {
	server := &http.Server{Addr: addr, Handler: s.Handler(path)}
	go func() {
		<-ctx.Done()
		_ = server.Shutdown(context.Background())
	}()
	err := server.ListenAndServe()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

func (s *Server) RunSSE(ctx context.Context, addr, ssePath, messagePath string) error {
	if ssePath == "" {
		ssePath = "/sse"
	}
	if messagePath == "" {
		messagePath = "/message"
	}
	mux := http.NewServeMux()
	mux.HandleFunc(ssePath, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		fmt.Fprintf(w, "event: endpoint\ndata: %s\n\n", messagePath)
	})
	mux.HandleFunc(messagePath, s.serveJSONRPC)
	server := &http.Server{Addr: addr, Handler: mux}
	go func() {
		<-ctx.Done()
		_ = server.Shutdown(context.Background())
	}()
	err := server.ListenAndServe()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

func (s *Server) serveJSONRPC(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	defer r.Body.Close()
	var req Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeHTTPResponse(w, newError(nil, -32700, err.Error()))
		return
	}
	writeHTTPResponse(w, s.Handle(r.Context(), req))
}

func writeResponse(writer *bufio.Writer, resp Response) {
	data, _ := json.Marshal(resp)
	_, _ = writer.Write(data)
	_ = writer.WriteByte('\n')
	_ = writer.Flush()
}

func writeHTTPResponse(w http.ResponseWriter, resp Response) {
	w.Header().Set("Content-Type", "application/json")
	if resp.JSONRPC == "" {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	_ = json.NewEncoder(w).Encode(resp)
}
