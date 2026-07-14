package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"mini-claude/internal/llm"
	"mini-claude/internal/permission"
	"mini-claude/internal/session"
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

func TestAgentAutoSavesNeutralSession(t *testing.T) {
	store := session.NewStoreAt(t.TempDir())
	fake := &llm.FakeClient{Responses: []llm.Response{{Message: llm.TextMessage(llm.RoleAssistant, "done"), Text: "done", Usage: llm.Usage{InputTokens: 4, OutputTokens: 2}}}}
	a := New(Options{Model: "fake", Client: fake, Output: &bytes.Buffer{}, SessionStore: store, SessionID: "saved", HomeDir: t.TempDir()})
	if err := a.ChatContext(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	data, err := store.Load("saved")
	if err != nil {
		t.Fatal(err)
	}
	if len(data.Messages) != 2 || data.Usage.InputTokens != 4 {
		t.Fatalf("data = %#v", data)
	}
	restored := New(Options{Model: "fake", Output: &bytes.Buffer{}})
	restored.RestoreSession(data)
	if len(restored.Messages()) != 2 || restored.TokenUsage().OutputTokens != 2 {
		t.Fatal("session was not restored")
	}
}

func TestAgentManualCompact(t *testing.T) {
	fake := &llm.FakeClient{Responses: []llm.Response{{Message: llm.TextMessage(llm.RoleAssistant, "summary"), Text: "summary"}}}
	a := New(Options{Model: "fake", Client: fake, Output: &bytes.Buffer{}})
	a.RestoreSession(session.Data{Metadata: session.Metadata{ID: "compact"}, Messages: []llm.Message{
		llm.TextMessage(llm.RoleUser, "one"), llm.TextMessage(llm.RoleAssistant, "two"),
		llm.TextMessage(llm.RoleUser, "three"), llm.TextMessage(llm.RoleAssistant, "four"),
	}})
	if err := a.Compact(); err != nil {
		t.Fatal(err)
	}
	if got := a.Messages(); len(got) != 2 || got[0].Role != llm.RoleUser {
		t.Fatalf("messages = %#v", got)
	}
}

type blockingClient struct{ started chan struct{} }

func (c *blockingClient) Complete(ctx context.Context, _ llm.Request) (llm.Response, error) {
	<-ctx.Done()
	return llm.Response{}, ctx.Err()
}

func (c *blockingClient) Stream(ctx context.Context, _ llm.Request, _ llm.EmitFunc) (llm.Response, error) {
	close(c.started)
	<-ctx.Done()
	return llm.Response{}, ctx.Err()
}

func TestAbortCancelsActiveChat(t *testing.T) {
	client := &blockingClient{started: make(chan struct{})}
	a := New(Options{Model: "fake", Client: client, Output: &bytes.Buffer{}})
	done := make(chan error, 1)
	go func() { done <- a.Chat("wait") }()
	<-client.started
	if !a.IsProcessing() {
		t.Fatal("agent should be processing")
	}
	a.Abort()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}
