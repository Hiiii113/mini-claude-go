package llm

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenAIStreamFixture(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %s", request.URL.Path)
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(writer, "data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"fixture\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"你\"},\"finish_reason\":null}]}\n\n")
		fmt.Fprint(writer, "data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"fixture\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"好\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2,\"total_tokens\":5}}\n\n")
		fmt.Fprint(writer, "data: [DONE]\n\n")
	}))
	defer server.Close()

	client := NewOpenAIClient(OpenAIOptions{APIKey: "test", BaseURL: server.URL + "/v1"})
	var deltas strings.Builder
	response, err := client.Stream(context.Background(), Request{Model: "fixture", Messages: []Message{TextMessage(RoleUser, "hi")}}, func(event StreamEvent) error {
		if event.Type == EventTextDelta {
			deltas.WriteString(event.Text)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Text != "你好" || deltas.String() != "你好" || response.Usage.InputTokens != 3 {
		t.Fatalf("response = %#v, deltas = %q", response, deltas.String())
	}
}

func TestAnthropicStreamFixture(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/messages" {
			t.Errorf("path = %s", request.URL.Path)
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		writeSSE := func(event, data string) { fmt.Fprintf(writer, "event: %s\ndata: %s\n\n", event, data) }
		writeSSE("message_start", `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"fixture","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":4,"output_tokens":0}}}`)
		writeSSE("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
		writeSSE("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"你好"}}`)
		writeSSE("content_block_stop", `{"type":"content_block_stop","index":0}`)
		writeSSE("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":2}}`)
		writeSSE("message_stop", `{"type":"message_stop"}`)
	}))
	defer server.Close()

	client := NewAnthropicClient(AnthropicOptions{APIKey: "test", BaseURL: server.URL})
	var deltas strings.Builder
	response, err := client.Stream(context.Background(), Request{Model: "fixture", MaxTokens: 100, Messages: []Message{TextMessage(RoleUser, "hi")}}, func(event StreamEvent) error {
		if event.Type == EventTextDelta {
			deltas.WriteString(event.Text)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Text != "你好" || deltas.String() != "你好" || response.Usage.InputTokens != 4 || response.Usage.OutputTokens != 2 {
		t.Fatalf("response = %#v, deltas = %q", response, deltas.String())
	}
}
