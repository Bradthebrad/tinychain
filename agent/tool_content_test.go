package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Bradthebrad/tinychain/anthropic"
	"github.com/Bradthebrad/tinychain/callbacks"
	"github.com/Bradthebrad/tinychain/lc"
	"github.com/Bradthebrad/tinychain/openai"
)

// testPNG is a small, valid PNG whose bytes differ by seed.
func testPNG(t testing.TB, seed int) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: uint8(seed), G: uint8(seed >> 8), A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

// imageTool returns text and images from CallContent, and only text from
// Call, like an MCP tool.
type imageTool struct {
	name   string
	text   string
	images func(call int) []ToolImage
	mu     sync.Mutex
	calls  int
}

func (t *imageTool) Definition() lc.ToolDefinition {
	return lc.ToolDefinition{Name: t.name, Description: "Take a screenshot.", ArgsSchema: ToolSchema(nil)}
}

func (t *imageTool) Call(context.Context, map[string]any) (string, error) {
	return "text only", nil
}

func (t *imageTool) CallContent(context.Context, map[string]any) (ToolOutput, error) {
	t.mu.Lock()
	t.calls++
	call := t.calls
	t.mu.Unlock()
	var images []ToolImage
	if t.images != nil {
		images = t.images(call)
	}
	return ToolOutput{Text: t.text, Images: images, Meta: map[string]any{"display": "main"}}, nil
}

func screenshotTool(t testing.TB) *imageTool {
	return &imageTool{name: "screenshot", text: "Took a screenshot.", images: func(call int) []ToolImage {
		return []ToolImage{{MediaType: "image/png", Data: testPNG(t, call)}}
	}}
}

// scriptModel asks for steps[i] on its i-th call, then answers, and keeps
// what it was sent.
type scriptModel struct {
	steps [][]lc.ToolCall
	seen  [][]lc.BaseMessage
}

func (m *scriptModel) Call(_ context.Context, messages []lc.BaseMessage, _ []Tool) (lc.BaseMessage, error) {
	m.seen = append(m.seen, append([]lc.BaseMessage(nil), messages...))
	if i := len(m.seen) - 1; i < len(m.steps) {
		return lc.BaseMessage{Type: lc.RoleAI, ToolCalls: m.steps[i]}, nil
	}
	return lc.AI("done"), nil
}

func (m *scriptModel) last() []lc.BaseMessage { return m.seen[len(m.seen)-1] }

func calls(names ...string) []lc.ToolCall {
	out := make([]lc.ToolCall, len(names))
	for i, name := range names {
		out[i] = lc.ToolCall{ID: fmt.Sprintf("call_%s_%d", name, i+1), Name: name, Args: map[string]any{}}
	}
	return out
}

func toolMessages(messages []lc.BaseMessage) []lc.BaseMessage {
	var out []lc.BaseMessage
	for _, msg := range messages {
		if msg.Type == lc.RoleTool {
			out = append(out, msg)
		}
	}
	return out
}

func imageData(content lc.Content) []string {
	var out []string
	for _, part := range content.Parts {
		if part.Type == "image" && part.Source != nil {
			out = append(out, part.Source.Data)
		}
	}
	return out
}

func compactJSON(t testing.TB, raw []byte) string {
	t.Helper()
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		t.Fatalf("compact %s: %v", raw, err)
	}
	return buf.String()
}

func TestCallToolContentUsesTheRichestCall(t *testing.T) {
	ctx := context.Background()
	plain := ToolFunc{Name: "plain", Func: func(context.Context, map[string]any) (string, error) { return "plain text", nil }}
	out, err := CallToolContent(ctx, plain, nil)
	if err != nil || out.Text != "plain text" || out.Images != nil || out.Meta != nil {
		t.Fatalf("plain = %#v, %v", out, err)
	}
	out, err = CallToolContent(ctx, metaTool{}, nil)
	if err != nil || out.Text != "meta text" || out.Meta["items"] != 2 {
		t.Fatalf("meta = %#v, %v", out, err)
	}
	shot := screenshotTool(t)
	out, err = CallToolContent(ctx, shot, nil)
	if err != nil || out.Text != "Took a screenshot." || len(out.Images) != 1 || out.Meta["display"] != "main" {
		t.Fatalf("content = %#v, %v", out, err)
	}
}

type metaTool struct{}

func (metaTool) Definition() lc.ToolDefinition { return lc.ToolDefinition{Name: "meta"} }
func (metaTool) Call(context.Context, map[string]any) (string, error) {
	return "", fmt.Errorf("Call should not be used")
}
func (metaTool) CallWithMeta(context.Context, map[string]any) (string, map[string]any, error) {
	return "meta text", map[string]any{"items": 2}, nil
}

func TestCheckToolImage(t *testing.T) {
	pngData := testPNG(t, 1)
	raw, _ := base64.StdEncoding.DecodeString(pngData)
	jpegHeader := base64.StdEncoding.EncodeToString([]byte("\xff\xd8\xff\xe0 jpeg body"))
	cases := []struct {
		name      string
		in        ToolImage
		wantType  string
		wantData  string
		wantError string
	}{
		{"png", ToolImage{MediaType: "image/png", Data: pngData}, "image/png", pngData, ""},
		{"data uri and line breaks", ToolImage{Data: "data:image/png;base64," + pngData[:20] + "\r\n" + pngData[20:]}, "image/png", pngData, ""},
		{"unpadded", ToolImage{MediaType: "image/png", Data: base64.RawStdEncoding.EncodeToString(raw)}, "image/png", pngData, ""},
		{"type from the data", ToolImage{MediaType: "image/png", Data: jpegHeader}, "image/jpeg", jpegHeader, ""},
		{"gif", ToolImage{MediaType: "image/gif", Data: base64.StdEncoding.EncodeToString([]byte("GIF89a...."))}, "image/gif", "", ""},
		{"webp", ToolImage{MediaType: "image/webp", Data: base64.StdEncoding.EncodeToString([]byte("RIFF\x10\x00\x00\x00WEBPVP8 "))}, "image/webp", "", ""},
		{"bmp", ToolImage{MediaType: "image/bmp", Data: base64.StdEncoding.EncodeToString([]byte("BM fake bitmap"))}, "", "", "not a PNG, JPEG, GIF or WebP image"},
		{"svg", ToolImage{MediaType: "image/svg+xml", Data: base64.StdEncoding.EncodeToString([]byte("<svg/>"))}, "", "", "not a PNG, JPEG, GIF or WebP image"},
		{"not base64", ToolImage{MediaType: "image/png", Data: "%%%"}, "", "", "not valid base64"},
		{"empty", ToolImage{MediaType: "image/png"}, "", "", "empty"},
		{"too large", ToolImage{MediaType: "image/png", Data: base64.StdEncoding.EncodeToString(append(raw, make([]byte, MaxToolImageBytes)...))}, "", "", "larger than 5.0 MB"},
	}
	for _, tc := range cases {
		got, err := CheckToolImage(tc.in)
		if tc.wantError != "" {
			if err == nil || err.Error() != tc.wantError {
				t.Errorf("%s: err = %v, want %q", tc.name, err, tc.wantError)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if got.MediaType != tc.wantType || (tc.wantData != "" && got.Data != tc.wantData) {
			t.Errorf("%s: got %s %q", tc.name, got.MediaType, got.Data)
		}
	}
}

func TestCheckToolImagesKeepsFourPerResult(t *testing.T) {
	var in []ToolImage
	for i := 0; i < 6; i++ {
		in = append(in, ToolImage{MediaType: "image/png", Data: testPNG(t, i)})
	}
	in = append([]ToolImage{{MediaType: "image/tiff", Data: base64.StdEncoding.EncodeToString([]byte("II*\x00tiff"))}}, in...)
	kept, notes := CheckToolImages(in)
	if len(kept) != MaxToolImagesPerResult {
		t.Fatalf("kept %d images", len(kept))
	}
	if kept[0].Data != in[1].Data || kept[3].Data != in[4].Data {
		t.Fatal("kept the wrong images")
	}
	want := []string{
		"[image image/tiff, 8 B, not shown: not a PNG, JPEG, GIF or WebP image]",
		fmt.Sprintf("[image image/png, %d B, not shown: at most 4 images per result]", in[5].Bytes()),
		fmt.Sprintf("[image image/png, %d B, not shown: at most 4 images per result]", in[6].Bytes()),
	}
	if strings.Join(notes, "|") != strings.Join(want, "|") {
		t.Fatalf("notes = %q", notes)
	}
}

func TestDescribeImages(t *testing.T) {
	kb := base64.StdEncoding.EncodeToString(make([]byte, 240<<10))
	if got := DescribeImages([]ToolImage{{MediaType: "image/png", Data: kb}}); got != "[1 image, image/png, 240 KB]" {
		t.Fatalf("one = %q", got)
	}
	if got := DescribeImages([]ToolImage{{MediaType: "image/png", Data: kb}, {MediaType: "image/jpeg", Data: kb}, {MediaType: "image/png", Data: kb}}); got != "[3 images, image/png + image/jpeg, 720 KB]" {
		t.Fatalf("three = %q", got)
	}
	if got := DescribeImages(nil); got != "" {
		t.Fatalf("none = %q", got)
	}
}

// fakeProvider answers each request with the next canned body and keeps the
// requests.
type fakeProvider struct {
	mu        sync.Mutex
	responses []string
	requests  [][]byte
}

func (p *fakeProvider) serve(t *testing.T) *httptest.Server {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		p.mu.Lock()
		p.requests = append(p.requests, body)
		n := len(p.requests)
		p.mu.Unlock()
		if n > len(p.responses) {
			t.Errorf("unexpected request %d: %s", n, body)
			http.Error(w, "no more responses", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(p.responses[n-1]))
	}))
	t.Cleanup(server.Close)
	return server
}

func TestAnthropicSendsToolImagesInToolResult(t *testing.T) {
	provider := &fakeProvider{responses: []string{
		`{"id":"msg_1","type":"message","role":"assistant","model":"claude-test","content":[{"type":"tool_use","id":"toolu_1","name":"screenshot","input":{}}],"stop_reason":"tool_use","usage":{"input_tokens":10,"output_tokens":5}}`,
		`{"id":"msg_2","type":"message","role":"assistant","model":"claude-test","content":[{"type":"text","text":"I see a window."}],"stop_reason":"end_turn","usage":{"input_tokens":20,"output_tokens":5}}`,
	}}
	server := provider.serve(t)
	shot := screenshotTool(t)
	a := New(Config{
		Model: AnthropicModel{Client: anthropic.Client{BaseURL: server.URL}, Model: "claude-test"},
		Tools: []Tool{shot},
	})
	result, err := a.Invoke(context.Background(), "What is on the screen?")
	if err != nil {
		t.Fatal(err)
	}
	if text := contentText(result.Output.Content); text != "I see a window." {
		t.Fatalf("output = %q", text)
	}
	var req struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(provider.requests[1], &req); err != nil {
		t.Fatal(err)
	}
	if len(req.Messages) != 3 {
		t.Fatalf("messages = %d", len(req.Messages))
	}
	want := `{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":[` +
		`{"type":"text","text":"Took a screenshot."},` +
		`{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + testPNG(t, 1) + `"}}]}]}`
	if got := compactJSON(t, req.Messages[2]); got != want {
		t.Fatalf("tool result message\n got %s\nwant %s", got, want)
	}
	if got := compactJSON(t, req.Messages[1]); got != `{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"screenshot"}]}` {
		t.Fatalf("assistant message = %s", got)
	}
}

// A turn with three calls: an image, plain text, then two images.
func chatToolTurn() string {
	return `{"id":"chatcmpl_1","choices":[{"index":0,"finish_reason":"tool_calls","message":{"role":"assistant","content":null,"tool_calls":[` +
		`{"id":"call_1","type":"function","function":{"name":"screenshot","arguments":"{}"}},` +
		`{"id":"call_2","type":"function","function":{"name":"echo","arguments":"{\"text\":\"hi\"}"}},` +
		`{"id":"call_3","type":"function","function":{"name":"zoom","arguments":"{}"}}]}}]}`
}

func turnTools(t testing.TB) []Tool {
	zoom := &imageTool{name: "zoom", images: func(int) []ToolImage {
		return []ToolImage{{MediaType: "image/png", Data: testPNG(t, 2)}, {MediaType: "image/png", Data: testPNG(t, 3)}}
	}}
	echo := ToolFunc{Name: "echo", Func: func(_ context.Context, args map[string]any) (string, error) { return stringArg(args, "text"), nil }}
	return []Tool{screenshotTool(t), echo, zoom}
}

func TestOpenAIChatSendsToolImagesInAUserMessageAfterTheToolMessages(t *testing.T) {
	provider := &fakeProvider{responses: []string{
		chatToolTurn(),
		`{"id":"chatcmpl_2","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"Zoomed in."}}]}`,
	}}
	server := provider.serve(t)
	a := New(Config{
		Model: OpenAIModel{Client: openai.Client{BaseURL: server.URL}, Model: "gpt-test"},
		Tools: turnTools(t),
	})
	if _, err := a.Invoke(context.Background(), "Zoom in."); err != nil {
		t.Fatal(err)
	}
	var req struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(provider.requests[1], &req); err != nil {
		t.Fatal(err)
	}
	if len(req.Messages) != 6 {
		t.Fatalf("messages = %d: %s", len(req.Messages), provider.requests[1])
	}
	dataURI := func(seed int) string { return "data:image/png;base64," + testPNG(t, seed) }
	want := []string{
		`{"role":"tool","content":"Took a screenshot.","tool_call_id":"call_1"}`,
		`{"role":"tool","content":"hi","tool_call_id":"call_2"}`,
		`{"role":"tool","content":"[The 2 images are in the next message.]","tool_call_id":"call_3"}`,
		`{"role":"user","content":[` +
			`{"type":"text","text":"Image from tool screenshot (call call_1):"},` +
			`{"image_url":{"url":"` + dataURI(1) + `"},"type":"image_url"},` +
			`{"type":"text","text":"Image from tool zoom (call call_3):"},` +
			`{"image_url":{"url":"` + dataURI(2) + `"},"type":"image_url"},` +
			`{"type":"text","text":"Image from tool zoom (call call_3):"},` +
			`{"image_url":{"url":"` + dataURI(3) + `"},"type":"image_url"}]}`,
	}
	for i, w := range want {
		if got := compactJSON(t, req.Messages[2+i]); got != w {
			t.Fatalf("message %d\n got %s\nwant %s", 2+i, got, w)
		}
	}
	if !strings.Contains(string(req.Messages[1]), `"role":"assistant"`) || !strings.Contains(string(req.Messages[1]), `"id":"call_3"`) {
		t.Fatalf("assistant message = %s", req.Messages[1])
	}
}

func TestOpenAIResponsesSendsToolImagesInFunctionCallOutput(t *testing.T) {
	provider := &fakeProvider{responses: []string{
		`{"id":"resp_1","object":"response","status":"completed","model":"gpt-test","output":[` +
			`{"type":"function_call","call_id":"call_1","name":"screenshot","arguments":"{}"},` +
			`{"type":"function_call","call_id":"call_2","name":"echo","arguments":"{\"text\":\"hi\"}"}]}`,
		`{"id":"resp_2","object":"response","status":"completed","model":"gpt-test","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}]}`,
	}}
	server := provider.serve(t)
	a := New(Config{
		Model: OpenAIModel{Client: openai.Client{BaseURL: server.URL}, Model: "gpt-test", UseResponses: true},
		Tools: turnTools(t),
	})
	if _, err := a.Invoke(context.Background(), "Look."); err != nil {
		t.Fatal(err)
	}
	var req struct {
		Input []json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(provider.requests[1], &req); err != nil {
		t.Fatal(err)
	}
	if len(req.Input) != 5 {
		t.Fatalf("input = %d: %s", len(req.Input), provider.requests[1])
	}
	want := []string{
		`{"type":"function_call_output","call_id":"call_1","output":[{"type":"input_text","text":"Took a screenshot."},{"type":"input_image","image_url":"data:image/png;base64,` + testPNG(t, 1) + `"}]}`,
		`{"type":"function_call_output","call_id":"call_2","output":"hi"}`,
	}
	for i, w := range want {
		if got := compactJSON(t, req.Input[3+i]); got != w {
			t.Fatalf("input %d\n got %s\nwant %s", 3+i, got, w)
		}
	}
}

func TestDisabledToolImagesBecomePlaceholders(t *testing.T) {
	model := &scriptModel{steps: [][]lc.ToolCall{calls("screenshot")}}
	shot := screenshotTool(t)
	log := &eventLog{}
	a := New(Config{Model: model, Tools: []Tool{shot}, Callbacks: log, ToolImages: ToolImagePolicy{Disabled: true}})
	if _, err := a.Invoke(context.Background(), "look"); err != nil {
		t.Fatal(err)
	}
	tools := toolMessages(model.last())
	want := fmt.Sprintf("Took a screenshot.\n[image image/png, %d B]", ToolImage{Data: testPNG(t, 1)}.Bytes())
	if len(tools) != 1 || tools[0].Content.Text == nil || *tools[0].Content.Text != want {
		t.Fatalf("tool message = %#v, want %q", tools, want)
	}
	// What the provider is sent is plain text too.
	blocks := anthropic.Messages(model.last())
	if got, _ := json.Marshal(blocks[len(blocks)-1]); !strings.Contains(string(got), `"content":"Took a screenshot.\n[image image/png,`) {
		t.Fatalf("anthropic message = %s", got)
	}
	end := log.events[len(log.events)-1]
	for _, e := range log.events {
		if e.Event == callbacks.EventToolEnd {
			end = e
		}
	}
	info, _ := end.Data.Extra["images"].(map[string]any)
	if info["count"] != 1 || info["sent"] != 0 {
		t.Fatalf("images info = %#v", end.Data.Extra)
	}
}

func TestKeepRecentZeroSendsNoToolImages(t *testing.T) {
	model := &scriptModel{steps: [][]lc.ToolCall{calls("screenshot")}}
	a := New(Config{Model: model, Tools: []Tool{screenshotTool(t)}, ToolImages: ToolImagePolicy{KeepRecent: new(0)}})
	if _, err := a.Invoke(context.Background(), "look"); err != nil {
		t.Fatal(err)
	}
	tools := toolMessages(model.last())
	if len(tools) != 1 || tools[0].Content.Text == nil || !strings.Contains(*tools[0].Content.Text, "[image image/png,") {
		t.Fatalf("tool message = %#v", tools)
	}
}

func TestOnlyTheMostRecentToolImagesAreSent(t *testing.T) {
	steps := [][]lc.ToolCall{}
	for i := 0; i < 5; i++ {
		steps = append(steps, []lc.ToolCall{{ID: fmt.Sprintf("call_%d", i+1), Name: "screenshot", Args: map[string]any{}}})
	}
	for _, tc := range []struct {
		name   string
		policy ToolImagePolicy
		keep   int
	}{
		{"default", ToolImagePolicy{}, 3},
		{"one", ToolImagePolicy{KeepRecent: new(1)}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := &scriptModel{steps: steps}
			a := New(Config{Model: model, Tools: []Tool{screenshotTool(t)}, ToolImages: tc.policy})
			result, err := a.Invoke(context.Background(), "watch the screen")
			if err != nil {
				t.Fatal(err)
			}
			tools := toolMessages(model.last())
			if len(tools) != 5 {
				t.Fatalf("tool messages = %d", len(tools))
			}
			for i, msg := range tools {
				data := imageData(msg.Content)
				if i < 5-tc.keep {
					if len(data) != 0 || msg.Content.Text == nil || *msg.Content.Text != "Took a screenshot.\n[earlier image from screenshot omitted]" {
						t.Fatalf("old result %d = %#v", i, msg.Content)
					}
					continue
				}
				if len(data) != 1 || data[0] != testPNG(t, i+1) {
					t.Fatalf("recent result %d has images %d", i, len(data))
				}
			}
			// Every model call was sent at most keep images.
			for n, sent := range model.seen {
				count := 0
				for _, msg := range toolMessages(sent) {
					count += imageCount(msg.Content)
				}
				if count > tc.keep {
					t.Fatalf("call %d was sent %d images", n, count)
				}
			}
			// The result carries no image data.
			data, _ := json.Marshal(result.Messages)
			if strings.Contains(string(data), testPNG(t, 5)) || strings.Contains(string(data), `"type":"image"`) {
				t.Fatalf("result messages carry an image: %s", data)
			}
			if !strings.Contains(string(data), "[image image/png,") {
				t.Fatalf("result messages lack the placeholder: %s", data)
			}
		})
	}
}

func TestKeepRecentCountsImagesWithinAResult(t *testing.T) {
	shot := testPNG(t, 1)
	messages := []lc.BaseMessage{
		lc.Human("look"),
		{Type: lc.RoleAI, ToolCalls: []lc.ToolCall{{ID: "a", Name: "zoom"}, {ID: "b", Name: "screenshot"}}},
		{Type: lc.RoleTool, ToolCallID: "a", Content: toolResultContent("two", []ToolImage{{MediaType: "image/png", Data: shot}, {MediaType: "image/png", Data: testPNG(t, 2)}})},
		{Type: lc.RoleTool, ToolCallID: "b", Content: toolResultContent("", []ToolImage{{MediaType: "image/png", Data: testPNG(t, 3)}, {MediaType: "image/png", Data: testPNG(t, 4)}})},
	}
	before, _ := json.Marshal(messages)
	out := keepRecentToolImages(messages, 3)
	if after, _ := json.Marshal(messages); string(after) != string(before) {
		t.Fatal("keepRecentToolImages modified its input")
	}
	first := out[2].Content
	if len(first.Parts) != 3 || first.Parts[1].Text != "[earlier image from zoom omitted]" || imageData(first)[0] != testPNG(t, 2) {
		t.Fatalf("first result = %#v", first)
	}
	if got := imageData(out[3].Content); len(got) != 2 {
		t.Fatalf("second result kept %d images", len(got))
	}
	if same := keepRecentToolImages(out, 3); &same[0] != &out[0] {
		t.Fatal("a second pass changed messages that were already within the limit")
	}
	none := keepRecentToolImages(messages, 0)
	if none[3].Content.Text == nil || *none[3].Content.Text != "[earlier image from screenshot omitted]\n[earlier image from screenshot omitted]" {
		t.Fatalf("keep 0 = %#v", none[3].Content)
	}
}

func TestToolImageCallbacksCarryNoImageData(t *testing.T) {
	model := &scriptModel{steps: [][]lc.ToolCall{calls("screenshot")}}
	log := &eventLog{}
	a := New(Config{Model: model, Tools: []Tool{screenshotTool(t)}, Callbacks: log})
	result, err := a.Invoke(context.Background(), "look")
	if err != nil {
		t.Fatal(err)
	}
	data := testPNG(t, 1)
	if len(imageData(toolMessages(model.last())[0].Content)) != 1 {
		t.Fatal("the model was not sent the image")
	}
	all, _ := json.Marshal(log.events)
	if strings.Contains(string(all), data) {
		t.Fatalf("a callback carries the image data: %s", all)
	}
	var end *callbacks.Event
	for i, e := range log.events {
		if e.Event == callbacks.EventToolEnd {
			end = &log.events[i]
		}
	}
	if end == nil {
		t.Fatal("no tool end event")
	}
	size := ToolImage{Data: data}.Bytes()
	if want := fmt.Sprintf("Took a screenshot.\n[1 image, image/png, %d B]", size); end.Data.Output != want {
		t.Fatalf("output = %q, want %q", end.Data.Output, want)
	}
	info, _ := end.Data.Extra["images"].(map[string]any)
	types, _ := info["media_types"].([]string)
	if info["count"] != 1 || info["sent"] != 1 || info["bytes"] != size || len(types) != 1 || types[0] != "image/png" || info["summary"] != fmt.Sprintf("[1 image, image/png, %d B]", size) {
		t.Fatalf("images info = %#v", info)
	}
	if meta, _ := end.Data.Extra["result_meta"].(map[string]any); meta["display"] != "main" {
		t.Fatalf("result meta = %#v", end.Data.Extra)
	}
	if out, _ := json.Marshal(result); strings.Contains(string(out), data) {
		t.Fatal("the result carries the image data")
	}
}

func TestImagesCountAsAFixedTokenCost(t *testing.T) {
	big := base64.StdEncoding.EncodeToString(append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 2<<20)...))
	text := lc.Tool("call_1", "Took a screenshot.")
	withImage := lc.BaseMessage{Type: lc.RoleTool, ToolCallID: "call_1", Content: toolResultContent("Took a screenshot.", []ToolImage{{MediaType: "image/png", Data: big}})}
	diff := EstimateTokens([]lc.BaseMessage{withImage}) - EstimateTokens([]lc.BaseMessage{text})
	if diff < imagePartTokens || diff > imagePartTokens+40 {
		t.Fatalf("a 2 MB image counts as %d tokens", diff)
	}
}

func TestShorteningAndSummariesDropToolImages(t *testing.T) {
	shot := testPNG(t, 7)
	call := lc.ToolCall{ID: "call_1", Name: "screenshot", Args: map[string]any{}}
	messages := []lc.BaseMessage{
		lc.Human("look"),
		{Type: lc.RoleAI, ToolCalls: []lc.ToolCall{call}},
		{Type: lc.RoleTool, ToolCallID: "call_1", Content: toolResultContent("Took a screenshot.", []ToolImage{{MediaType: "image/png", Data: shot}})},
		lc.AI("I see it."),
	}
	policy := normalizeContextPolicy(ContextPolicy{Enabled: true, MaxTokens: 8000})
	meter := newContextMeter(policy, nil, nil)
	out, changed := shortenSeenToolResults(messages, meter, 0, policy)
	if changed == 0 || imageCount(out[2].Content) != 0 {
		t.Fatalf("shortening kept the image: %#v", out[2].Content)
	}
	if text := contentText(out[2].Content); !strings.Contains(text, "1 non-text part(s), not shown") {
		t.Fatalf("digest = %q", text)
	}
	transcript := formatMessagesForCompaction(messages, policy)
	if strings.Contains(transcript, shot) || !strings.Contains(transcript, "Took a screenshot.\n[1 image(s), not shown]") {
		t.Fatalf("transcript = %q", transcript)
	}
}

func TestSubagentsFollowTheParentImagePolicy(t *testing.T) {
	sub := &scriptModel{steps: [][]lc.ToolCall{calls("screenshot")}}
	parent := &scriptModel{steps: [][]lc.ToolCall{{{ID: "task_1", Name: "task", Args: map[string]any{"description": "look", "subagent_type": "viewer"}}}}}
	a := New(Config{
		Model:      parent,
		ToolImages: ToolImagePolicy{Disabled: true},
		Subagents:  []Subagent{{Name: "viewer", Model: sub, Tools: []Tool{screenshotTool(t)}}},
	})
	if _, err := a.Invoke(context.Background(), "delegate"); err != nil {
		t.Fatal(err)
	}
	tools := toolMessages(sub.last())
	if len(tools) != 1 || imageCount(tools[0].Content) != 0 || !strings.Contains(contentText(tools[0].Content), "[image image/png,") {
		t.Fatalf("subagent tool message = %#v", tools)
	}
}
