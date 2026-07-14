package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscoverProjectOverridesAndResolve(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	writeSkill := func(base, description, contextMode string) {
		dir := filepath.Join(base, ".claude", "skills", "commit")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		content := "---\nname: commit\ndescription: " + description + "\ncontext: " + contextMode + "\nallowed-tools: [read_file, grep_search]\n---\nRun $ARGUMENTS in ${CLAUDE_SKILL_DIR}"
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeSkill(home, "user", "inline")
	writeSkill(cwd, "project", "fork")
	registry := NewRegistry(home, cwd)
	skill, ok := registry.Get("commit")
	if !ok || skill.Source != "project" || skill.Context != ContextFork || len(skill.AllowedTools) != 2 {
		t.Fatalf("skill = %#v", skill)
	}
	resolved := Resolve(skill, "fix types")
	if !strings.Contains(resolved, "fix types") || !strings.Contains(resolved, skill.Dir) {
		t.Fatalf("resolved = %q", resolved)
	}
}

func TestRefreshFindsNewSkill(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	registry := NewRegistry(home, cwd)
	if len(registry.Discover()) != 0 {
		t.Fatal("expected empty registry")
	}
	dir := filepath.Join(cwd, ".claude", "skills", "new")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("body"), 0o600); err != nil {
		t.Fatal(err)
	}
	registry.Refresh()
	if len(registry.Discover()) != 1 {
		t.Fatal("refresh did not find skill")
	}
}
