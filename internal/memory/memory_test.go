package memory

import (
	"context"
	"strings"
	"testing"

	"mini-claude/internal/llm"
)

func TestSaveListAndIndex(t *testing.T) {
	store := NewStoreAt(t.TempDir())
	filename, err := store.Save(Entry{Name: "用户偏好", Description: "使用中文", Type: TypeUser, Content: "回答使用中文"})
	if err != nil {
		t.Fatal(err)
	}
	items, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Filename != filename || !strings.Contains(store.LoadIndex(), "用户偏好") {
		t.Fatalf("items=%#v index=%q", items, store.LoadIndex())
	}
}

func TestSemanticSelectionAndFormatting(t *testing.T) {
	store := NewStoreAt(t.TempDir())
	filename, err := store.Save(Entry{Name: "Go style", Description: "Go formatting preference", Type: TypeFeedback, Content: "Use gofmt."})
	if err != nil {
		t.Fatal(err)
	}
	fake := &llm.FakeClient{Responses: []llm.Response{{Text: `{"selected_memories":["` + filename + `"]}`}}}
	items, err := store.SelectRelevant(context.Background(), "please format Go", fake, "fake", map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || !strings.Contains(FormatInjection(items), "Use gofmt") {
		t.Fatalf("items=%#v", items)
	}
}

func TestProjectPathChangesMemoryDirectory(t *testing.T) {
	home := t.TempDir()
	if NewStore(home, "a").Dir() == NewStore(home, "b").Dir() {
		t.Fatal("project directories should be isolated")
	}
}
