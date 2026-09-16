package openai

import (
	"context"
	"fmt"
	"github.com/Bradthebrad/tinychain/streaming"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestChatStreamBeforeCompletionAndToolAssembly(t *testing.T) {
	received := make(chan struct{})
	var text string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hi \",\"reasoning\":\"PRIVATE\",\"tool_calls\":[{\"index\":0,\"id\":\"call\",\"type\":\"function\",\"function\":{\"name\":\"test\",\"arguments\":\"{\"}}]}}]}\n\n")
		w.(http.Flusher).Flush()
		<-received
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"there\",\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"}\"}}]}}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	ctx := streaming.WithSink(context.Background(), func(d streaming.Delta) {
		if d.Summary {
			t.Fatal("raw reasoning leaked")
		}
		text += d.Text
		if text == "Hi " {
			close(received)
		}
	})
	out, err := (Client{BaseURL: server.URL}).ChatCompletion(ctx, ChatCompletionRequest{Model: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if text != "Hi there" || out.Choices[0].Message.ToolCalls[0].Function.Arguments != "{}" {
		t.Fatalf("unexpected result: %s %#v", text, out)
	}
}

func TestResponsesStreamRejectsTruncation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n")
	}))
	defer server.Close()
	ctx := streaming.WithSink(context.Background(), func(streaming.Delta) {})
	if _, err := (Client{BaseURL: server.URL}).Responses(ctx, ResponsesRequest{}); err == nil {
		t.Fatal("accepted truncated response")
	}
}
