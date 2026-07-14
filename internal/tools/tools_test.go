package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mini-claude/internal/llm"
)

func execute(t *testing.T, registry *Registry, name string, input any) (string, error) {
	t.Helper()
	data, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	return registry.Execute(context.Background(), llm.ToolCall{Name: name, Arguments: data})
}

func TestReadBeforeEditAndQuoteNormalization(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "sample.txt")
	if err := os.WriteFile(path, []byte(`const value = "Hello World"`), 0o644); err != nil {
		t.Fatal(err)
	}
	registry := DefaultRegistry(root)
	if _, err := execute(t, registry, "edit_file", map[string]any{
		"file_path": path, "old_string": `"Hello World"`, "new_string": `"Hi"`,
	}); err == nil || !strings.Contains(err.Error(), "must read") {
		t.Fatalf("expected read-before-edit error, got %v", err)
	}
	if _, err := execute(t, registry, "read_file", map[string]any{"file_path": path}); err != nil {
		t.Fatal(err)
	}
	result, err := execute(t, registry, "edit_file", map[string]any{
		"file_path": path, "old_string": "“Hello World”", "new_string": `"Hi"`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "quote normalization") {
		t.Fatalf("result = %q", result)
	}
}

func TestToolSearchActivatesDeferredTools(t *testing.T) {
	registry := DefaultRegistry(t.TempDir())
	for _, definition := range registry.Definitions() {
		if definition.Name == "enter_plan_mode" {
			t.Fatal("deferred tool was active before search")
		}
	}
	if _, err := execute(t, registry, "tool_search", map[string]any{"query": "plan mode"}); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, definition := range registry.Definitions() {
		found = found || definition.Name == "enter_plan_mode"
	}
	if !found {
		t.Fatal("deferred plan tools were not activated")
	}
}
