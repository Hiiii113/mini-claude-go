package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

type fakeAgent struct {
	chats   []string
	cleared int
	costs   int
	err     error
}

func (a *fakeAgent) Chat(input string) error {
	a.chats = append(a.chats, input)
	return a.err
}

func (a *fakeAgent) ClearHistory() { a.cleared++ }
func (a *fakeAgent) ShowCost()     { a.costs++ }

func TestRunDispatchesCommandsAndChat(t *testing.T) {
	a := &fakeAgent{}
	var output bytes.Buffer
	input := strings.NewReader("/clear\n/cost\nhello\nexit\n")

	if err := Run(input, &output, a); err != nil {
		t.Fatal(err)
	}
	if a.cleared != 1 || a.costs != 1 {
		t.Fatalf("clear=%d cost=%d", a.cleared, a.costs)
	}
	if len(a.chats) != 1 || a.chats[0] != "hello" {
		t.Fatalf("chats = %#v", a.chats)
	}
	if !strings.Contains(output.String(), "Bye!") {
		t.Fatalf("output = %q", output.String())
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
