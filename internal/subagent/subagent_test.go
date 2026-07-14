package subagent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBuiltInAgentsHaveExpectedTools(t *testing.T) {
	registry := NewRegistry(t.TempDir(), t.TempDir())
	if got := registry.Resolve("explore"); len(got.AllowedTools) != 3 {
		t.Fatalf("explore = %#v", got)
	}
	if got := registry.Resolve("general"); len(got.AllowedTools) != 0 {
		t.Fatalf("general should use all non-agent tools: %#v", got)
	}
}

func TestProjectCustomAgentOverridesUser(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	write := func(base, description string) {
		dir := filepath.Join(base, ".claude", "agents")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		content := "---\nname: reviewer\ndescription: " + description + "\nallowed-tools: read_file, grep_search\n---\nReview carefully."
		if err := os.WriteFile(filepath.Join(dir, "reviewer.md"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(home, "user")
	write(cwd, "project")
	got := NewRegistry(home, cwd).Resolve("reviewer")
	if got.Description != "project" || len(got.AllowedTools) != 2 || got.SystemPrompt != "Review carefully." {
		t.Fatalf("got %#v", got)
	}
}
