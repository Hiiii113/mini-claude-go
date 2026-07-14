package contextmgr

import (
	"context"
	"strings"
	"testing"
	"time"

	"mini-claude/internal/llm"
)

func TestPrepareBudgetsAndSnipsOldResults(t *testing.T) {
	manager := NewWithWindow(100)
	messages := make([]llm.Message, 0, 5)
	for index := 0; index < 5; index++ {
		text := "result"
		if index == 4 {
			text = strings.Repeat("x", 20_000)
		}
		messages = append(messages, llm.ToolResultMessage("id", "read_file", text, false))
	}
	got := manager.Prepare(messages, 75)
	if got[0].Blocks[0].Text != snipPlaceholder || got[1].Blocks[0].Text != snipPlaceholder {
		t.Fatalf("old results were not snipped: %#v", got)
	}
	if !strings.Contains(got[4].Blocks[0].Text, "budgeted") {
		t.Fatalf("large result was not budgeted")
	}
	if messages[0].Blocks[0].Text == snipPlaceholder {
		t.Fatal("Prepare mutated caller history")
	}
}

func TestPrepareMicrocompactsAfterIdle(t *testing.T) {
	now := time.Now()
	manager := NewWithWindow(1000)
	manager.now = func() time.Time { return now }
	manager.MarkAPICall()
	now = now.Add(6 * time.Minute)
	var messages []llm.Message
	for index := 0; index < 5; index++ {
		messages = append(messages, llm.ToolResultMessage("id", "web_fetch", "result", false))
	}
	got := manager.Prepare(messages, 0)
	if got[0].Blocks[0].Text != clearedPlaceholder || got[2].Blocks[0].Text == clearedPlaceholder {
		t.Fatalf("unexpected microcompact result: %#v", got)
	}
}

func TestCompactPreservesLastUserMessage(t *testing.T) {
	fake := &llm.FakeClient{Responses: []llm.Response{{Text: "summary", Message: llm.TextMessage(llm.RoleAssistant, "summary"), Usage: llm.Usage{InputTokens: 10, OutputTokens: 2}}}}
	messages := []llm.Message{
		llm.TextMessage(llm.RoleUser, "one"), llm.TextMessage(llm.RoleAssistant, "two"),
		llm.TextMessage(llm.RoleUser, "three"), llm.TextMessage(llm.RoleUser, "current"),
	}
	got, usage, changed, err := NewWithWindow(100).Compact(context.Background(), fake, "fake", "system", messages)
	if err != nil {
		t.Fatal(err)
	}
	if !changed || len(got) != 3 || !strings.Contains(got[0].Blocks[0].Text, "summary") || got[2].Blocks[0].Text != "current" || usage.OutputTokens != 2 {
		t.Fatalf("got %#v, usage %#v, changed %v", got, usage, changed)
	}
}
