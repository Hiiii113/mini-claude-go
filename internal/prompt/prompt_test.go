package prompt

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadProjectInstructionsAndIncludes(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "project", "sub")
	if err := os.MkdirAll(filepath.Join(child, ".claude", "rules"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		filepath.Join(root, "CLAUDE.md"):                 "root rule",
		filepath.Join(root, "project", "CLAUDE.md"):      "@./details.md",
		filepath.Join(root, "project", "details.md"):     "project details",
		filepath.Join(child, ".claude", "rules", "a.md"): "local rule",
	}
	for path, content := range files {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got, err := LoadProjectInstructions(child)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"root rule", "project details", "local rule"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}
}

func TestCircularIncludeIsReported(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("@./a.md"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.md"), []byte("@./a.md"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadProjectInstructions(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "circular include") {
		t.Fatalf("got %q", got)
	}
}

func TestBuildIncludesInjectedSections(t *testing.T) {
	got, err := Build(context.Background(), Options{WorkingDir: t.TempDir(), Memory: "remember", Skills: "skill-a", DeferredTools: []string{"z", "a"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"remember", "skill-a", "a, z"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q", want)
		}
	}
}
