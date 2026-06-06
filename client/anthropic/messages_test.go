package anthropic

import (
	"encoding/json"
	"testing"

	"tinychain/lc"
)

func TestMessagesSeparateSystemAndToolResult(t *testing.T) {
	input := []lc.BaseMessage{
		lc.System("be brief"),
		lc.Human("hi"),
		lc.Tool("toolu_1", "42"),
	}

	system := SystemFromMessages(input)
	systemJSON, err := json.Marshal(system)
	if err != nil {
		t.Fatal(err)
	}
	if string(systemJSON) != `"be brief"` {
		t.Fatalf("system JSON = %s", systemJSON)
	}

	messages := Messages(input)
	if len(messages) != 2 {
		t.Fatalf("message count = %d", len(messages))
	}
	if messages[1].Content.Blocks[0].Type != "tool_result" {
		t.Fatalf("tool block type = %q", messages[1].Content.Blocks[0].Type)
	}
}
