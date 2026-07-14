package llm

import (
	"context"
	"testing"
)

func TestFakeClientRecordsRequestsAndStreams(t *testing.T) {
	fake := &FakeClient{Responses: []Response{{Text: "hello"}}}
	var text string
	_, err := fake.Stream(context.Background(), Request{Model: "test"}, func(event StreamEvent) error {
		text += event.Text
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if text != "hello" || len(fake.Requests) != 1 {
		t.Fatalf("text=%q requests=%d", text, len(fake.Requests))
	}
}
