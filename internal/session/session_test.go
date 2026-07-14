package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"mini-claude/internal/llm"
)

func TestSaveLoadAndLatest(t *testing.T) {
	store := NewStoreAt(t.TempDir())
	older := Data{Metadata: Metadata{ID: "old", Model: "m", StartTime: time.Now().Add(-time.Hour)}, Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "old")}}
	newer := Data{Metadata: Metadata{ID: "new", Model: "m", StartTime: time.Now()}, Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "new")}, Usage: llm.Usage{InputTokens: 12}}
	if err := store.Save(older); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	if err := store.Save(newer); err != nil {
		t.Fatal(err)
	}
	got, err := store.Latest()
	if err != nil {
		t.Fatal(err)
	}
	if got.Metadata.ID != "new" || got.Version != currentVersion || got.Usage.InputTokens != 12 {
		t.Fatalf("got %#v", got)
	}
}

func TestLoadLegacyOpenAI(t *testing.T) {
	dir := t.TempDir()
	legacy := `{"metadata":{"id":"legacy","model":"gpt","cwd":".","startTime":"2026-01-01T00:00:00Z","messageCount":3},"openaiMessages":[{"role":"system","content":"sys"},{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"read_file","arguments":"{\"file_path\":\"a.go\"}"}}]},{"role":"tool","tool_call_id":"c1","content":"data"}]}`
	if err := os.WriteFile(filepath.Join(dir, "legacy.json"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := NewStoreAt(dir).Load("legacy")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Messages) != 3 || got.Messages[1].Blocks[0].Type != llm.BlockToolCall || got.Messages[2].Blocks[0].Type != llm.BlockToolResult {
		t.Fatalf("messages %#v", got.Messages)
	}
	if got.Metadata.StartTime.IsZero() {
		t.Fatal("legacy startTime was not migrated")
	}
}

func TestLoadLegacyAnthropic(t *testing.T) {
	dir := t.TempDir()
	legacy := `{"metadata":{"id":"legacy","model":"claude","cwd":".","startTime":"2026-01-01T00:00:00Z","messageCount":2},"anthropicMessages":[{"role":"assistant","content":[{"type":"tool_use","id":"c1","name":"read_file","input":{"file_path":"a.go"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"c1","content":"data"}]}]}`
	if err := os.WriteFile(filepath.Join(dir, "legacy.json"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := NewStoreAt(dir).Load("legacy")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Messages) != 2 || got.Messages[0].Blocks[0].ToolName != "read_file" || got.Messages[1].Role != llm.RoleTool {
		t.Fatalf("messages %#v", got.Messages)
	}
}
