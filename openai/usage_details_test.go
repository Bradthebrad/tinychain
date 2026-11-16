package openai

import (
	"encoding/json"
	"testing"
)

// Providers add nested objects to the usage breakdown; a response must
// still decode, keeping every count it can.
func TestUsageDetailsTakeNewShapes(t *testing.T) {
	var r ResponsesResponse
	data := `{"id":"resp_1","status":"completed","output":[],"usage":{"input_tokens":120,"output_tokens":30,"total_tokens":150,` +
		`"input_tokens_details":{"cached_tokens":64,"cache_breakdown":{"read":64,"write":0},"note":"x","ratio":0.5,"missing":null},` +
		`"output_tokens_details":{"reasoning_tokens":12}}}`
	if err := json.Unmarshal([]byte(data), &r); err != nil {
		t.Fatalf("decode: %v", err)
	}
	in := r.Usage.InputTokensDetails
	if in["cached_tokens"] != 64 || in["cache_breakdown.read"] != 64 || in["ratio"] != 0 || len(in) != 4 {
		t.Fatalf("input details: %v", in)
	}
	if r.Usage.OutputTokensDetails["reasoning_tokens"] != 12 || r.Usage.InputTokens != 120 {
		t.Fatalf("usage: %+v", r.Usage)
	}
	for _, odd := range []string{`null`, `[]`, `"text"`, `7`} {
		var u Usage
		if err := json.Unmarshal([]byte(`{"input_tokens":1,"input_tokens_details":`+odd+`}`), &u); err != nil || u.InputTokens != 1 {
			t.Errorf("%s: %v %+v", odd, err, u)
		}
	}
}

func TestStreamEventKeepsTheRestOfAnOddEvent(t *testing.T) {
	var e struct {
		Type     string             `json:"type"`
		Response *ResponsesResponse `json:"response"`
	}
	if err := decodeStreamEvent([]byte(`{"type":"response.completed","response":{"id":"r","status":"completed","temperature":"warm"}}`), &e); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if e.Type != "response.completed" || e.Response == nil || e.Response.ID != "r" {
		t.Fatalf("event: %+v", e)
	}
	if err := decodeStreamEvent([]byte(`{"type":`), &e); err == nil {
		t.Fatal("broken JSON was accepted")
	}
}
