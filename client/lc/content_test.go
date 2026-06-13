package lc

import (
	"encoding/json"
	"testing"
)

func TestContentMarshalsTextOrParts(t *testing.T) {
	text, err := json.Marshal(TextContent("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if string(text) != `"hello"` {
		t.Fatalf("text content JSON = %s", text)
	}

	parts, err := json.Marshal(PartsContent(ContentPart{Type: "text", Text: "hello"}))
	if err != nil {
		t.Fatal(err)
	}
	if string(parts) != `[{"type":"text","text":"hello"}]` {
		t.Fatalf("parts content JSON = %s", parts)
	}
}

func TestContentPartMergesExtraFields(t *testing.T) {
	data, err := json.Marshal(ContentPart{
		Type:  "image_url",
		Extra: map[string]any{"image_url": map[string]any{"url": "data:image/png;base64,abc"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); got != `{"image_url":{"url":"data:image/png;base64,abc"},"type":"image_url"}` {
		t.Fatalf("part JSON = %s", got)
	}
}
