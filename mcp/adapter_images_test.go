package mcp

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image"
	"image/png"
	"strings"
	"testing"

	"github.com/Bradthebrad/tinychain/agent"
	"github.com/Bradthebrad/tinychain/lc"
)

func pngBase64(t testing.TB, width int) string {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewGray(image.Rect(0, 0, width, 1))); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func TestResultContentSeparatesImagesFromText(t *testing.T) {
	shot := pngBase64(t, 3)
	jpeg := b64("\xff\xd8\xff\xe0 jpeg")
	out := ResultContent(ToolResult{Content: []Content{
		{Type: "text", Text: "Screen 1 of 2"},
		{Type: "image", MimeType: "image/png", Data: shot},
		{Type: "audio", MimeType: "audio/wav", Data: b64("RIFF....WAVE")},
		{Type: "image", MimeType: "image/jpg", Data: jpeg},
		{Type: "resource_link", Name: "log", URI: "file:///tmp/log.txt"},
	}})
	if len(out.Images) != 2 {
		t.Fatalf("images = %#v", out.Images)
	}
	if out.Images[0] != (agent.ToolImage{MediaType: "image/png", Data: shot}) || out.Images[1] != (agent.ToolImage{MediaType: "image/jpeg", Data: jpeg}) {
		t.Fatalf("images = %#v", out.Images)
	}
	if want := "Screen 1 of 2\n[audio audio/wav, 12 B]\n[resource log file:///tmp/log.txt]"; out.Text != want {
		t.Fatalf("text = %q, want %q", out.Text, want)
	}
	// Without images, a result reads as ResultText does.
	plain := ToolResult{Content: []Content{{Type: "text", Text: "a"}, {Type: "resource", Resource: &ResourceContents{URI: "x", Text: "b"}}}, StructuredContent: map[string]any{"n": 1}}
	if got := ResultContent(plain); got.Text != ResultText(plain) || got.Images != nil {
		t.Fatalf("plain = %#v", got)
	}
	structured := ToolResult{StructuredContent: map[string]any{"items": []any{1, 2}}}
	if got := ResultContent(structured).Text; got != `{"items":[1,2]}` {
		t.Fatalf("structured = %q", got)
	}
}

func TestResultContentLimits(t *testing.T) {
	var content []Content
	for i := 1; i <= 5; i++ {
		content = append(content, Content{Type: "image", MimeType: "image/png", Data: pngBase64(t, i)})
	}
	big := base64.StdEncoding.EncodeToString(append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, agent.MaxToolImageBytes)...))
	content = append([]Content{
		{Type: "text", Text: "many"},
		{Type: "image", MimeType: "image/png", Data: big},
		{Type: "image", MimeType: "image/svg+xml", Data: b64("<svg/>")},
		{Type: "image", MimeType: "image/png", Data: "not base64!"},
	}, content...)
	out := ResultContent(ToolResult{Content: content})
	if len(out.Images) != agent.MaxToolImagesPerResult {
		t.Fatalf("kept %d images", len(out.Images))
	}
	for i, img := range out.Images {
		if img.Data != pngBase64(t, i+1) {
			t.Fatalf("image %d is the wrong one", i)
		}
	}
	lines := strings.Split(out.Text, "\n")
	want := []string{
		"many",
		"[image image/png, 5.0 MB, not shown: larger than 5.0 MB]",
		"[image image/svg+xml, 6 B, not shown: not a PNG, JPEG, GIF or WebP image]",
		"[image image/png, 8 B, not shown: not valid base64]",
		fmt.Sprintf("[image image/png, %d B, not shown: at most 4 images per result]", (agent.ToolImage{Data: pngBase64(t, 5)}).Bytes()),
	}
	if strings.Join(lines, "|") != strings.Join(want, "|") {
		t.Fatalf("text =\n%s\nwant\n%s", out.Text, strings.Join(want, "\n"))
	}
}

// serverTransport sends a client's requests straight to a server.
type serverTransport struct{ server *Server }

func (t serverTransport) RoundTrip(ctx context.Context, req Request) (Response, error) {
	return t.server.Handle(ctx, req), nil
}

func (serverTransport) Close() error { return nil }

func TestRemoteToolCallContent(t *testing.T) {
	shot := pngBase64(t, 4)
	server := NewServer("screen")
	server.AddTool(Tool{Name: "screenshot", Handler: func(context.Context, map[string]any) (ToolResult, error) {
		return ToolResult{Content: []Content{{Type: "text", Text: "1280x800"}, {Type: "image", MimeType: "image/png", Data: shot}}}, nil
	}})
	server.AddTool(Tool{Name: "broken", Handler: func(context.Context, map[string]any) (ToolResult, error) {
		return ToolResult{Content: []Content{{Type: "text", Text: "no display"}, {Type: "image", MimeType: "image/png", Data: shot}}, IsError: true}, nil
	}})
	client := NewClient(serverTransport{server})
	tool := &RemoteTool{Client: client, Spec: Tool{Name: "screenshot"}, ServerID: "screen"}
	var _ agent.ContentTool = tool

	out, err := agent.CallToolContent(context.Background(), tool, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.Text != "1280x800" || len(out.Images) != 1 || out.Images[0].Data != shot || out.Images[0].MediaType != "image/png" {
		t.Fatalf("output = %#v", out)
	}
	if types, _ := out.Meta["content_types"].(map[string]int); types["image"] != 1 || types["text"] != 1 {
		t.Fatalf("meta = %#v", out.Meta)
	}
	// Call and CallWithMeta still describe the image in text.
	text, _, err := tool.CallWithMeta(context.Background(), nil)
	if err != nil || !strings.HasPrefix(text, "1280x800\n[image image/png, ") {
		t.Fatalf("CallWithMeta = %q, %v", text, err)
	}

	broken := &RemoteTool{Client: client, Spec: Tool{Name: "broken"}}
	out, err = broken.CallContent(context.Background(), nil)
	if err == nil || !strings.HasPrefix(err.Error(), "no display\n[image image/png, ") || out.Images != nil || out.Meta["is_error"] != true {
		t.Fatalf("error result = %#v, %v", out, err)
	}
	if _, err := (&RemoteTool{Spec: Tool{Name: "x"}}).CallContent(context.Background(), nil); err == nil {
		t.Fatal("a tool without a client succeeded")
	}
}

type imageAgentTool struct{ data string }

func (imageAgentTool) Definition() lc.ToolDefinition { return lc.ToolDefinition{Name: "shot"} }
func (imageAgentTool) Call(context.Context, map[string]any) (string, error) {
	return "text only", nil
}
func (t imageAgentTool) CallContent(context.Context, map[string]any) (agent.ToolOutput, error) {
	return agent.ToolOutput{Images: []agent.ToolImage{{MediaType: "image/png", Data: t.data}}}, nil
}

func TestToolFromAgentServesImages(t *testing.T) {
	shot := pngBase64(t, 2)
	result, err := ToolFromAgent(imageAgentTool{data: shot}).Handler(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Content) != 1 || result.Content[0] != (Content{Type: "image", MimeType: "image/png", Data: shot}) {
		t.Fatalf("content = %#v", result.Content)
	}
}
