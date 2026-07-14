package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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

func TestWebFetchConvertsHTMLToReadableText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(writer, `<html><head><title>ignored</title><style>.x{color:red}</style></head><body><h1>Mini Claude</h1><p>hello &amp; goodbye</p><script>alert(1)</script></body></html>`)
	}))
	defer server.Close()

	result, err := execute(t, DefaultRegistry(t.TempDir()), "web_fetch", map[string]any{"url": server.URL})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "Mini Claude") || !strings.Contains(result, "hello & goodbye") || strings.Contains(result, "alert(1)") {
		t.Fatalf("result=%q", result)
	}
}

func TestGrepSearchSupportsRegexAndRecursiveInclude(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "nested", "main.go"), []byte("package demo\nfunc Hello123() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "nested", "ignore.txt"), []byte("Hello456\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := execute(t, DefaultRegistry(root), "grep_search", map[string]any{"pattern": `Hello\d+`, "include": "**/*.go"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "nested/main.go:2:func Hello123") || strings.Contains(result, "ignore.txt") {
		t.Fatalf("result=%q", result)
	}
}

func TestWriteFileCreatesNestedDirectoryAndLimitsPreview(t *testing.T) {
	root := t.TempDir()
	lines := make([]string, 35)
	for index := range lines {
		lines[index] = fmt.Sprintf("line-%02d", index+1)
	}
	path := filepath.Join(root, "new", "nested", "file.txt")
	result, err := execute(t, DefaultRegistry(root), "write_file", map[string]any{"file_path": path, "content": strings.Join(lines, "\n")})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != strings.Join(lines, "\n") || !strings.Contains(result, "(35 lines)") || !strings.Contains(result, "... (35 lines total)") || strings.Contains(result, "line-31") {
		t.Fatalf("result=%q bytes=%d", result, len(data))
	}
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
	if !strings.Contains(result, "- \"Hello World\"") || !strings.Contains(result, "+ \"Hi\"") {
		t.Fatalf("missing edit preview: %q", result)
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
