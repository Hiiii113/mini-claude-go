package ui

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"mini-claude/internal/agent"
)

func TestRedirectedRendererHasNoANSIOrSpinner(t *testing.T) {
	var output bytes.Buffer
	renderer := NewRenderer(&output)
	renderer.HandleAgentEvent(agent.Event{Type: agent.EventModelStart})
	renderer.HandleAgentEvent(agent.Event{Type: agent.EventToolStart, ToolName: "read_file", Arguments: json.RawMessage(`{"file_path":"main.go"}`)})
	renderer.HandleAgentEvent(agent.Event{Type: agent.EventToolResult, ToolName: "read_file", Result: "content"})
	got := output.String()
	if strings.Contains(got, "\x1b[") || strings.Contains(got, "Thinking") || !strings.Contains(got, "main.go") {
		t.Fatalf("output = %q", got)
	}
}
