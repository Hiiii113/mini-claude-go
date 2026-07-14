package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"mini-claude/internal/llm"
	"mini-claude/internal/permission"
	"mini-claude/internal/tools"
)

func TestAgentLoopExecutesToolAndContinues(t *testing.T) {
	arguments := json.RawMessage(`{"value":"ok"}`)
	fake := &llm.FakeClient{Responses: []llm.Response{
		{
			Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.ContentBlock{{
				Type: llm.BlockToolCall, ToolCallID: "call-1", ToolName: "echo", Arguments: arguments,
			}}},
			ToolCalls: []llm.ToolCall{{ID: "call-1", Name: "echo", Arguments: arguments}},
			Usage:     llm.Usage{InputTokens: 10, OutputTokens: 5},
		},
		{Message: llm.TextMessage(llm.RoleAssistant, "done"), Text: "done", Usage: llm.Usage{InputTokens: 20, OutputTokens: 3}},
	}}
	registry := tools.NewRegistry(t.TempDir())
	registry.Register(llm.ToolDefinition{Name: "echo", InputSchema: map[string]any{"type": "object"}},
		func(_ context.Context, raw json.RawMessage) (string, error) { return string(raw), nil }, true)
	var output bytes.Buffer
	a := New(Options{
		Model: "fake", Client: fake, Tools: registry, Output: &output,
		Permissions: permission.New(permission.ModeBypassPermissions, t.TempDir(), t.TempDir()),
	})
	if err := a.ChatContext(context.Background(), "start"); err != nil {
		t.Fatal(err)
	}
	if len(fake.Requests) != 2 {
		t.Fatalf("requests = %d", len(fake.Requests))
	}
	history := a.Messages()
	if len(history) != 4 || history[2].Blocks[0].Type != llm.BlockToolResult {
		t.Fatalf("history = %#v", history)
	}
	if usage := a.TokenUsage(); usage.InputTokens != 30 || usage.OutputTokens != 8 {
		t.Fatalf("usage = %#v", usage)
	}
}
