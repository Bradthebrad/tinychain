// Package streaming carries opt-in, public output deltas through model calls.
package streaming

import (
	"bufio"
	"context"
	"io"
	"strings"
)

type Delta struct {
	Text    string
	Summary bool
}
type key struct{}

func WithSink(ctx context.Context, sink func(Delta)) context.Context {
	return context.WithValue(ctx, key{}, sink)
}
func Enabled(ctx context.Context) bool { _, ok := ctx.Value(key{}).(func(Delta)); return ok }
func Emit(ctx context.Context, text string, summary bool) {
	if f, ok := ctx.Value(key{}).(func(Delta)); ok && text != "" {
		f(Delta{text, summary})
	}
}

// ReadSSE dispatches complete frames immediately, preserving whitespace in JSON values.
func ReadSSE(r io.Reader, handle func([]byte) error) error {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 4096), 16*1024*1024)
	var data []string
	flush := func() error {
		if len(data) == 0 {
			return nil
		}
		b := []byte(strings.Join(data, "\n"))
		data = nil
		return handle(b)
	}
	for s.Scan() {
		line := s.Text()
		if line == "" {
			if err := flush(); err != nil {
				return err
			}
		} else if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if err := s.Err(); err != nil {
		return err
	}
	return flush()
}
