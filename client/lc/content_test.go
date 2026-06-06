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
