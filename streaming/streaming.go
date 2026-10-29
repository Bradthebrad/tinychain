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
	// Commentary is text the model said on its way to its answer, such as
	// before using a tool, rather than the answer itself.
	Commentary bool
	// Step is a tool call the model ran itself; Text is empty.
	Step *Step
}

// Step is a tool call made and run by a model that is an agent of its own,
// such as a command-line agent used as a model. It is reported so it shows
// like the calling agent's own tool calls; it is never run again.
type Step struct {
	ID       string
	Name     string
	Input    map[string]any
	Output   string
	Error    string
	Done     bool
	Metadata map[string]any
}

type key struct{}

func WithSink(ctx context.Context, sink func(Delta)) context.Context {
	return context.WithValue(ctx, key{}, sink)
}
func Enabled(ctx context.Context) bool { _, ok := ctx.Value(key{}).(func(Delta)); return ok }
func Emit(ctx context.Context, text string, summary bool) {
	if f, ok := ctx.Value(key{}).(func(Delta)); ok && text != "" {
		f(Delta{Text: text, Summary: summary})
	}
}

// EmitCommentary reports text the model said on its way to its answer.
func EmitCommentary(ctx context.Context, text string) {
	if f, ok := ctx.Value(key{}).(func(Delta)); ok && text != "" {
		f(Delta{Text: text, Commentary: true})
	}
}

// EmitStep reports a tool call the model ran itself: once when it starts
// and once, with Done, when it ends.
func EmitStep(ctx context.Context, step Step) {
	if f, ok := ctx.Value(key{}).(func(Delta)); ok && step.Name != "" {
		f(Delta{Step: &step})
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
