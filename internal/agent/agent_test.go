package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mini-claude/internal/llm"
	"mini-claude/internal/memory"
	"mini-claude/internal/permission"
	"mini-claude/internal/session"
	"mini-claude/internal/skills"
	"mini-claude/internal/subagent"
	"mini-claude/internal/tools"
)

func TestAgentLoopExecutesToolAndContinues(t *testing.T) {
	arguments := json.RawMessage(`{"value":"ok"}`)
	fake := &llm.FakeClient{Responses: []llm.Response{
		{
			Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.ContentBlock{{
				Type: llm.BlockToolCall, ToolCallID: "call-1", ToolName: "echo", Arguments: arguments,
			}}},
			ToolCalls: []llm.ToolCall{{ID: "call-1", Name: "echo", Arguments: arguments}},
			Usage:     llm.Usage{InputTokens: 10, OutputTokens: 5},
		},
		{Message: llm.TextMessage(llm.RoleAssistant, "done"), Text: "done", Usage: llm.Usage{InputTokens: 20, OutputTokens: 3}},
	}}
	registry := tools.NewRegistry(t.TempDir())
	registry.Register(llm.ToolDefinition{Name: "echo", InputSchema: map[string]any{"type": "object"}},
		func(_ context.Context, raw json.RawMessage) (string, error) { return string(raw), nil }, true)
	var output bytes.Buffer
	a := New(Options{
		Model: "fake", Client: fake, Tools: registry, Output: &output,
		Permissions: permission.New(permission.ModeBypassPermissions, t.TempDir(), t.TempDir()),
	})
	if err := a.ChatContext(context.Background(), "start"); err != nil {
		t.Fatal(err)
	}
	if len(fake.Requests) != 2 {
		t.Fatalf("requests = %d", len(fake.Requests))
	}
	history := a.Messages()
	if len(history) != 4 || history[2].Blocks[0].Type != llm.BlockToolResult {
		t.Fatalf("history = %#v", history)
	}
	if usage := a.TokenUsage(); usage.InputTokens != 30 || usage.OutputTokens != 8 {
		t.Fatalf("usage = %#v", usage)
	}
}

func TestLargeToolResultPersistsFullOutput(t *testing.T) {
	home := t.TempDir()
	full := strings.Repeat("complete-line-abcdefghijklmnopqrstuvwxyz\n", 2000)
	registry := tools.NewRegistry(t.TempDir())
	registry.Register(llm.ToolDefinition{Name: "large"}, func(context.Context, json.RawMessage) (string, error) {
		return full, nil
	}, true)
	a := New(Options{
		Tools: registry, HomeDir: home, Output: &bytes.Buffer{},
		Permissions: permission.New(permission.ModeBypassPermissions, home, t.TempDir()),
	})
	result := a.executeToolCall(context.Background(), llm.ToolCall{Name: "large", Arguments: json.RawMessage(`{}`)})
	if result.err != nil || !strings.Contains(result.content, "Full output saved to") {
		t.Fatalf("result=%#v", result)
	}
	matches, err := filepath.Glob(filepath.Join(home, ".mini-claude", "tool-results", "*-large.txt"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("matches=%v err=%v", matches, err)
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != full {
		t.Fatalf("saved bytes=%d want=%d", len(data), len(full))
	}
}

func TestMaxTurnsStopsBeforeAnotherModelCall(t *testing.T) {
	arguments := json.RawMessage(`{}`)
	fake := &llm.FakeClient{Responses: []llm.Response{{
		Message:   llm.Message{Role: llm.RoleAssistant, Blocks: []llm.ContentBlock{{Type: llm.BlockToolCall, ToolCallID: "one", ToolName: "noop", Arguments: arguments}}},
		ToolCalls: []llm.ToolCall{{ID: "one", Name: "noop", Arguments: arguments}},
	}}}
	registry := tools.NewRegistry(t.TempDir())
	registry.Register(llm.ToolDefinition{Name: "noop"}, func(context.Context, json.RawMessage) (string, error) { return "ok", nil }, true)
	a := New(Options{Model: "fake", Client: fake, Tools: registry, MaxTurns: 1, Output: &bytes.Buffer{}, Permissions: permission.New(permission.ModeBypassPermissions, t.TempDir(), t.TempDir())})
	err := a.ChatContext(context.Background(), "start")
	if err == nil || !strings.Contains(err.Error(), "turn limit reached") || len(fake.Requests) != 1 {
		t.Fatalf("err=%v requests=%d", err, len(fake.Requests))
	}
}

func TestAgentAutoSavesNeutralSession(t *testing.T) {
	store := session.NewStoreAt(t.TempDir())
	fake := &llm.FakeClient{Responses: []llm.Response{{Message: llm.TextMessage(llm.RoleAssistant, "done"), Text: "done", Usage: llm.Usage{InputTokens: 4, OutputTokens: 2}}}}
	a := New(Options{Model: "fake", Client: fake, Output: &bytes.Buffer{}, SessionStore: store, SessionID: "saved", HomeDir: t.TempDir()})
	if err := a.ChatContext(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	data, err := store.Load("saved")
	if err != nil {
		t.Fatal(err)
	}
	if len(data.Messages) != 2 || data.Usage.InputTokens != 4 {
		t.Fatalf("data = %#v", data)
	}
	restored := New(Options{Model: "fake", Output: &bytes.Buffer{}})
	restored.RestoreSession(data)
	if len(restored.Messages()) != 2 || restored.TokenUsage().OutputTokens != 2 {
		t.Fatal("session was not restored")
	}
}

func TestAgentManualCompact(t *testing.T) {
	fake := &llm.FakeClient{Responses: []llm.Response{{Message: llm.TextMessage(llm.RoleAssistant, "summary"), Text: "summary"}}}
	a := New(Options{Model: "fake", Client: fake, Output: &bytes.Buffer{}})
	a.RestoreSession(session.Data{Metadata: session.Metadata{ID: "compact"}, Messages: []llm.Message{
		llm.TextMessage(llm.RoleUser, "one"), llm.TextMessage(llm.RoleAssistant, "two"),
		llm.TextMessage(llm.RoleUser, "three"), llm.TextMessage(llm.RoleAssistant, "four"),
	}})
	if err := a.Compact(); err != nil {
		t.Fatal(err)
	}
	if got := a.Messages(); len(got) != 2 || got[0].Role != llm.RoleUser {
		t.Fatalf("messages = %#v", got)
	}
}

type blockingClient struct{ started chan struct{} }

func (c *blockingClient) Complete(ctx context.Context, _ llm.Request) (llm.Response, error) {
	<-ctx.Done()
	return llm.Response{}, ctx.Err()
}

func (c *blockingClient) Stream(ctx context.Context, _ llm.Request, _ llm.EmitFunc) (llm.Response, error) {
	close(c.started)
	<-ctx.Done()
	return llm.Response{}, ctx.Err()
}

func TestAbortCancelsActiveChat(t *testing.T) {
	client := &blockingClient{started: make(chan struct{})}
	a := New(Options{Model: "fake", Client: client, Output: &bytes.Buffer{}})
	done := make(chan error, 1)
	go func() { done <- a.Chat("wait") }()
	<-client.started
	if !a.IsProcessing() {
		t.Fatal("agent should be processing")
	}
	a.Abort()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestMemoryPrefetchInjectsRelevantMemory(t *testing.T) {
	store := memory.NewStoreAt(t.TempDir())
	filename, err := store.Save(memory.Entry{Name: "Go style", Description: "Go formatting", Type: memory.TypeFeedback, Content: "Always run gofmt."})
	if err != nil {
		t.Fatal(err)
	}
	fake := &llm.FakeClient{Responses: []llm.Response{
		{Text: `{"selected_memories":["` + filename + `"]}`},
		{Message: llm.TextMessage(llm.RoleAssistant, "done"), Text: "done"},
	}}
	a := New(Options{Model: "fake", Client: fake, Memories: store, Output: &bytes.Buffer{}})
	if err := a.ChatContext(context.Background(), "please format Go"); err != nil {
		t.Fatal(err)
	}
	if got := a.Messages()[0].Blocks[0].Text; !strings.Contains(got, "Always run gofmt") || !strings.Contains(got, "please format Go") {
		t.Fatalf("user message = %q", got)
	}
}

func TestSkillInlineAndForkExecution(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	writeSkill := func(name, mode string) {
		dir := filepath.Join(cwd, ".claude", "skills", name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		content := "---\nname: " + name + "\ncontext: " + mode + "\n---\nSkill prompt $ARGUMENTS"
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeSkill("inline", "inline")
	writeSkill("forked", "fork")
	registry := skills.NewRegistry(home, cwd)
	a := New(Options{Model: "fake", Client: &llm.FakeClient{}, Skills: registry, Tools: tools.DefaultRegistry(cwd), Output: &bytes.Buffer{}})
	inline, err := a.executeSkillTool(context.Background(), map[string]any{"skill_name": "inline", "args": "ARG"})
	if err != nil || !strings.Contains(inline, "Skill prompt ARG") {
		t.Fatalf("inline=%q err=%v", inline, err)
	}
	fake := &llm.FakeClient{Responses: []llm.Response{{Message: llm.TextMessage(llm.RoleAssistant, "fork done"), Text: "fork done", Usage: llm.Usage{InputTokens: 3, OutputTokens: 2}}}}
	a.client = fake
	forked, err := a.executeSkillTool(context.Background(), map[string]any{"skill_name": "forked", "args": "ARG"})
	if err != nil || !strings.Contains(forked, "fork done") {
		t.Fatalf("forked=%q err=%v", forked, err)
	}
}

func TestPlanModeApprovalChoices(t *testing.T) {
	tests := []struct {
		choice   PlanChoice
		wantMode permission.Mode
		reset    bool
	}{
		{PlanClearAndExecute, permission.ModeAcceptEdits, true},
		{PlanExecute, permission.ModeAcceptEdits, false},
		{PlanManualExecute, permission.ModeDefault, false},
		{PlanKeepPlanning, permission.ModePlan, false},
	}
	for _, test := range tests {
		t.Run(string(test.choice), func(t *testing.T) {
			home := t.TempDir()
			a := New(Options{Model: "fake", PermissionMode: string(permission.ModePlan), HomeDir: home, WorkingDir: t.TempDir(), Output: &bytes.Buffer{}})
			if err := os.WriteFile(a.PlanFilePath(), []byte("approved plan"), 0o600); err != nil {
				t.Fatal(err)
			}
			a.SetPlanApproval(func(context.Context, string) (PlanDecision, error) {
				return PlanDecision{Choice: test.choice, Feedback: "revise"}, nil
			})
			result, err := a.executePlanTool(context.Background(), "exit_plan_mode")
			if err != nil {
				t.Fatal(err)
			}
			if a.permissions.Mode != test.wantMode {
				t.Fatalf("mode=%s result=%q", a.permissions.Mode, result)
			}
			if (a.pendingReset != nil) != test.reset {
				t.Fatalf("pending reset=%v", a.pendingReset)
			}
			if test.choice == PlanKeepPlanning && !strings.Contains(result, "revise") {
				t.Fatalf("result=%q", result)
			}
		})
	}
}

func TestPlanModeOnlyAllowsPlanFileWrite(t *testing.T) {
	a := New(Options{Model: "fake", PermissionMode: string(permission.ModePlan), HomeDir: t.TempDir(), WorkingDir: t.TempDir(), Output: &bytes.Buffer{}})
	if got := a.permissions.Check("write_file", map[string]any{"file_path": a.PlanFilePath()}); got.Action != permission.ActionAllow {
		t.Fatalf("plan file decision=%#v", got)
	}
	if got := a.permissions.Check("write_file", map[string]any{"file_path": filepath.Join(t.TempDir(), "other.md")}); got.Action != permission.ActionDeny {
		t.Fatalf("other file decision=%#v", got)
	}
}

func TestPlanClearApprovalResetsToValidHistory(t *testing.T) {
	arguments := json.RawMessage(`{}`)
	fake := &llm.FakeClient{Responses: []llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.ContentBlock{{Type: llm.BlockToolCall, ToolCallID: "p1", ToolName: "exit_plan_mode", Arguments: arguments}}}, ToolCalls: []llm.ToolCall{{ID: "p1", Name: "exit_plan_mode", Arguments: arguments}}},
		{Message: llm.TextMessage(llm.RoleAssistant, "executing"), Text: "executing"},
	}}
	a := New(Options{Model: "fake", Client: fake, PermissionMode: string(permission.ModePlan), HomeDir: t.TempDir(), WorkingDir: t.TempDir(), Output: &bytes.Buffer{}})
	if err := os.WriteFile(a.PlanFilePath(), []byte("step one"), 0o600); err != nil {
		t.Fatal(err)
	}
	a.SetPlanApproval(func(context.Context, string) (PlanDecision, error) {
		return PlanDecision{Choice: PlanClearAndExecute}, nil
	})
	if err := a.ChatContext(context.Background(), "finish plan"); err != nil {
		t.Fatal(err)
	}
	if len(fake.Requests) != 2 || len(fake.Requests[1].Messages) != 1 || fake.Requests[1].Messages[0].Role != llm.RoleUser || !strings.Contains(fake.Requests[1].Messages[0].Blocks[0].Text, "step one") {
		t.Fatalf("second request=%#v", fake.Requests[1])
	}
}

func TestWritingMemoryFileRefreshesIndex(t *testing.T) {
	store := memory.NewStoreAt(t.TempDir())
	registry := tools.DefaultRegistry(t.TempDir())
	a := New(Options{Tools: registry, Memories: store, Permissions: permission.New(permission.ModeBypassPermissions, t.TempDir(), t.TempDir()), Output: &bytes.Buffer{}})
	path := filepath.Join(store.Dir(), "project_decision.md")
	content := "---\nname: Decision\ndescription: Chosen design\ntype: project\n---\nUse a modular monolith."
	arguments, _ := json.Marshal(map[string]any{"file_path": path, "content": content})
	result := a.executeToolCall(context.Background(), llm.ToolCall{ID: "m1", Name: "write_file", Arguments: arguments})
	if result.err != nil {
		t.Fatal(result.err)
	}
	if !strings.Contains(store.LoadIndex(), "Decision") {
		t.Fatalf("index=%q", store.LoadIndex())
	}
}

func TestSubAgentUsesIsolatedHistoryAndAddsUsage(t *testing.T) {
	arguments := json.RawMessage(`{"description":"inspect","prompt":"find code","type":"explore"}`)
	fake := &llm.FakeClient{Responses: []llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.ContentBlock{{Type: llm.BlockToolCall, ToolCallID: "a1", ToolName: "agent", Arguments: arguments}}}, ToolCalls: []llm.ToolCall{{ID: "a1", Name: "agent", Arguments: arguments}}, Usage: llm.Usage{InputTokens: 1, OutputTokens: 1}},
		{Message: llm.TextMessage(llm.RoleAssistant, "child answer"), Text: "child answer", Usage: llm.Usage{InputTokens: 5, OutputTokens: 2}},
		{Message: llm.TextMessage(llm.RoleAssistant, "parent done"), Text: "parent done", Usage: llm.Usage{InputTokens: 7, OutputTokens: 3}},
	}}
	cwd := t.TempDir()
	a := New(Options{Model: "fake", Client: fake, Tools: tools.DefaultRegistry(cwd), SubAgents: subagent.NewRegistry(t.TempDir(), cwd), PermissionMode: string(permission.ModeBypassPermissions), Output: &bytes.Buffer{}})
	if err := a.ChatContext(context.Background(), "parent task"); err != nil {
		t.Fatal(err)
	}
	if len(fake.Requests) != 3 {
		t.Fatalf("requests=%d", len(fake.Requests))
	}
	for _, definition := range fake.Requests[1].Tools {
		if definition.Name == "agent" {
			t.Fatal("child received recursive agent tool")
		}
	}
	if got := a.Messages()[2].Blocks[0].Text; !strings.Contains(got, "child answer") {
		t.Fatalf("tool result=%q", got)
	}
	if usage := a.TokenUsage(); usage.InputTokens != 13 || usage.OutputTokens != 6 {
		t.Fatalf("usage=%#v", usage)
	}
}

type cancelChildClient struct {
	mu           sync.Mutex
	calls        int
	childStarted chan struct{}
}

func (c *cancelChildClient) Complete(ctx context.Context, request llm.Request) (llm.Response, error) {
	return c.Stream(ctx, request, nil)
}
func (c *cancelChildClient) Stream(ctx context.Context, _ llm.Request, emit llm.EmitFunc) (llm.Response, error) {
	c.mu.Lock()
	c.calls++
	call := c.calls
	c.mu.Unlock()
	if call == 1 {
		tool := llm.ToolCall{ID: "a1", Name: "agent", Arguments: json.RawMessage(`{"description":"wait","prompt":"wait","type":"general"}`)}
		if emit != nil {
			_ = emit(llm.StreamEvent{Type: llm.EventToolCall, ToolCall: &tool})
		}
		return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.ContentBlock{{Type: llm.BlockToolCall, ToolCallID: tool.ID, ToolName: tool.Name, Arguments: tool.Arguments}}}, ToolCalls: []llm.ToolCall{tool}}, nil
	}
	close(c.childStarted)
	<-ctx.Done()
	return llm.Response{}, ctx.Err()
}

func TestParentAbortCancelsSubAgent(t *testing.T) {
	client := &cancelChildClient{childStarted: make(chan struct{})}
	cwd := t.TempDir()
	a := New(Options{Model: "fake", Client: client, Tools: tools.DefaultRegistry(cwd), SubAgents: subagent.NewRegistry(t.TempDir(), cwd), PermissionMode: string(permission.ModeBypassPermissions), Output: &bytes.Buffer{}})
	done := make(chan error, 1)
	go func() { done <- a.Chat("start") }()
	<-client.childStarted
	a.Abort()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
}

func TestGeneralSubAgentDoesNotBypassParentPermission(t *testing.T) {
	arguments := json.RawMessage(`{"command":"rm protected-file"}`)
	fake := &llm.FakeClient{Responses: []llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.ContentBlock{{Type: llm.BlockToolCall, ToolCallID: "s1", ToolName: "run_shell", Arguments: arguments}}}, ToolCalls: []llm.ToolCall{{ID: "s1", Name: "run_shell", Arguments: arguments}}},
		{Message: llm.TextMessage(llm.RoleAssistant, "handled denial"), Text: "handled denial"},
	}}
	registry := tools.NewRegistry(t.TempDir())
	executed := false
	registry.Register(llm.ToolDefinition{Name: "run_shell"}, func(context.Context, json.RawMessage) (string, error) { executed = true; return "ran", nil }, false)
	confirmed := false
	a := New(Options{Model: "fake", Client: fake, Tools: registry, Permissions: permission.New(permission.ModeDefault, t.TempDir(), t.TempDir()), Confirm: func(context.Context, string) (bool, error) { confirmed = true; return false, nil }, Output: &bytes.Buffer{}})
	result, err := a.runSubAgent(context.Background(), subagent.Config{Name: "general"}, "try command")
	if err != nil {
		t.Fatal(err)
	}
	if !confirmed || executed || !strings.Contains(result, "handled denial") {
		t.Fatalf("confirmed=%v executed=%v result=%q", confirmed, executed, result)
	}
}

func TestConcurrencySafeToolsRunInParallelAndKeepOrder(t *testing.T) {
	registry := tools.NewRegistry(t.TempDir())
	started := make(chan string, 2)
	release := make(chan struct{})
	for _, name := range []string{"first", "second"} {
		name := name
		registry.Register(llm.ToolDefinition{Name: name}, func(context.Context, json.RawMessage) (string, error) {
			started <- name
			<-release
			return name, nil
		}, true)
	}
	a := New(Options{Tools: registry, Permissions: permission.New(permission.ModeBypassPermissions, t.TempDir(), t.TempDir()), Output: &bytes.Buffer{}})
	done := make(chan []toolExecution, 1)
	go func() {
		done <- a.executeToolCalls(context.Background(), []llm.ToolCall{{Name: "first", Arguments: json.RawMessage(`{}`)}, {Name: "second", Arguments: json.RawMessage(`{}`)}})
	}()
	for count := 0; count < 2; count++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("safe tools did not start concurrently")
		}
	}
	close(release)
	results := <-done
	if results[0].content != "first" || results[1].content != "second" {
		t.Fatalf("results=%#v", results)
	}
}

func TestWriteToolsRemainSerial(t *testing.T) {
	registry := tools.NewRegistry(t.TempDir())
	var active, maximum int32
	for _, name := range []string{"write1", "write2"} {
		registry.Register(llm.ToolDefinition{Name: name}, func(context.Context, json.RawMessage) (string, error) {
			current := atomic.AddInt32(&active, 1)
			for {
				old := atomic.LoadInt32(&maximum)
				if current <= old || atomic.CompareAndSwapInt32(&maximum, old, current) {
					break
				}
			}
			time.Sleep(10 * time.Millisecond)
			atomic.AddInt32(&active, -1)
			return "ok", nil
		}, false)
	}
	a := New(Options{Tools: registry, Permissions: permission.New(permission.ModeBypassPermissions, t.TempDir(), t.TempDir()), Output: &bytes.Buffer{}})
	a.executeToolCalls(context.Background(), []llm.ToolCall{{Name: "write1", Arguments: json.RawMessage(`{}`)}, {Name: "write2", Arguments: json.RawMessage(`{}`)}})
	if maximum != 1 {
		t.Fatalf("maximum concurrency=%d", maximum)
	}
}
