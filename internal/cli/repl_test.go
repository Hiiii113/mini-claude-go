package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeAgent struct {
	chats    []string
	cleared  int
	costs    int
	err      error
	compacts int
	plans    int
	confirm  func(context.Context, string) (bool, error)
}

func (a *fakeAgent) Chat(input string) error {
	a.chats = append(a.chats, input)
	return a.err
}

func (a *fakeAgent) ClearHistory()                                             { a.cleared++ }
func (a *fakeAgent) ShowCost()                                                 { a.costs++ }
func (a *fakeAgent) Compact() error                                            { a.compacts++; return nil }
func (a *fakeAgent) TogglePlanMode() string                                    { a.plans++; return "plan" }
func (a *fakeAgent) SetConfirm(fn func(context.Context, string) (bool, error)) { a.confirm = fn }

func TestRunDispatchesCommandsAndChat(t *testing.T) {
	a := &fakeAgent{}
	var output bytes.Buffer
	input := strings.NewReader("/clear\n/cost\n/compact\n/plan\n/memory\n/skills\nhello\nexit\n")

	if err := Run(input, &output, a); err != nil {
		t.Fatal(err)
	}
	if a.cleared != 1 || a.costs != 1 {
		t.Fatalf("clear=%d cost=%d", a.cleared, a.costs)
	}
	if a.compacts != 1 || a.plans != 1 {
		t.Fatalf("compact=%d plan=%d", a.compacts, a.plans)
	}
	if len(a.chats) != 1 || a.chats[0] != "hello" {
		t.Fatalf("chats = %#v", a.chats)
	}
	if !strings.Contains(output.String(), "Bye!") {
		t.Fatalf("output = %q", output.String())
	}
}

type confirmingAgent struct {
	fakeAgent
	allowed bool
}

func (a *confirmingAgent) Chat(input string) error {
	a.chats = append(a.chats, input)
	var err error
	a.allowed, err = a.confirm(context.Background(), "dangerous")
	return err
}

func TestConfirmationUsesSameInputStream(t *testing.T) {
	a := &confirmingAgent{}
	var output bytes.Buffer
	if err := Run(strings.NewReader("hello\ny\nexit\n"), &output, a); err != nil {
		t.Fatal(err)
	}
	if !a.allowed || !strings.Contains(output.String(), "Allow?") {
		t.Fatalf("allowed=%v output=%q", a.allowed, output.String())
	}
}

func TestRunPrintsChatErrorsAndContinues(t *testing.T) {
	a := &fakeAgent{err: errors.New("boom")}
	var output bytes.Buffer

	if err := Run(strings.NewReader("hello\nexit\n"), &output, a); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "error: boom") {
		t.Fatalf("output = %q", output.String())
	}
}
