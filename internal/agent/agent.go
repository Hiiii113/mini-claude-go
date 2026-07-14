package agent

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"mini-claude/internal/contextmgr"
	"mini-claude/internal/llm"
	"mini-claude/internal/memory"
	"mini-claude/internal/permission"
	"mini-claude/internal/session"
	"mini-claude/internal/skills"
	"mini-claude/internal/subagent"
	"mini-claude/internal/tools"
)

const (
	defaultMaxTokens   = int64(16384)
	largeResultLimit   = 30 * 1024
	largeResultPreview = 200
)

// ConfirmFunc 在权限层要求确认时询问用户。
type ConfirmFunc func(ctx context.Context, message string) (bool, error)

// EventType 是 Agent 向终端 UI 发出的生命周期事件。
type EventType string

const (
	EventModelStart    EventType = "model_start"
	EventModelOutput   EventType = "model_output"
	EventToolStart     EventType = "tool_start"
	EventToolResult    EventType = "tool_result"
	EventSubAgentStart EventType = "subagent_start"
	EventSubAgentEnd   EventType = "subagent_end"
)

// Event 携带 spinner 和工具摘要渲染所需的最小信息。
type Event struct {
	Type      EventType
	ToolName  string
	Arguments json.RawMessage
	Result    string
	IsError   bool
}

// EventFunc 接收 Agent 生命周期事件。
type EventFunc func(Event)

// PlanChoice 是退出计划模式时用户选择的审批结果。
type PlanChoice string

const (
	PlanClearAndExecute PlanChoice = "clear-and-execute"
	PlanExecute         PlanChoice = "execute"
	PlanManualExecute   PlanChoice = "manual-execute"
	PlanKeepPlanning    PlanChoice = "keep-planning"
)

// PlanDecision 包含审批选择和拒绝后的修改意见。
type PlanDecision struct {
	Choice   PlanChoice
	Feedback string
}

// PlanApprovalFunc 展示计划并取得用户审批。
type PlanApprovalFunc func(context.Context, string) (PlanDecision, error)

// Options 配置 Agent 的模型、工具、权限、预算和输出目标。
type Options struct {
	Model          string
	PermissionMode string
	Client         llm.Client
	Tools          *tools.Registry
	Permissions    *permission.Evaluator
	SystemPrompt   string
	Output         io.Writer
	Confirm        ConfirmFunc
	Thinking       bool
	MaxCostUSD     float64
	MaxTurns       int
	MaxTokens      int64
	HomeDir        string
	WorkingDir     string
	Context        *contextmgr.Manager
	SessionStore   *session.Store
	SessionID      string
	Events         EventFunc
	Memories       *memory.Store
	Skills         *skills.Registry
	SubAgents      *subagent.Registry
	PlanApproval   PlanApprovalFunc
}

// Agent 是与模型厂商无关的核心 Agent Loop。
type Agent struct {
	model        string
	client       llm.Client
	tools        *tools.Registry
	permissions  *permission.Evaluator
	system       string
	output       io.Writer
	confirm      ConfirmFunc
	thinking     bool
	maxCostUSD   float64
	maxTurns     int
	maxTokens    int64
	homeDir      string
	workingDir   string
	context      *contextmgr.Manager
	sessions     *session.Store
	sessionID    string
	sessionStart time.Time

	mu              sync.RWMutex
	outputMu        sync.Mutex
	messages        []llm.Message
	inputTokens     int64
	outputTokens    int64
	turns           int
	lastInputTokens int64

	processMu          sync.Mutex
	currentCancel      context.CancelFunc
	prePlanMode        permission.Mode
	events             EventFunc
	baseSystem         string
	memories           *memory.Store
	skills             *skills.Registry
	subagents          *subagent.Registry
	planApproval       PlanApprovalFunc
	planFilePath       string
	pendingReset       []llm.Message
	pendingClearStats  bool
	surfacedMemories   map[string]bool
	sessionMemoryBytes int
}

// New 创建 Agent；外部依赖通过 Options 注入以便离线测试。
func New(options Options) *Agent {
	output := options.Output
	if output == nil {
		output = os.Stdout
	}
	maxTokens := options.MaxTokens
	if maxTokens <= 0 {
		maxTokens = defaultMaxTokens
	}
	home := options.HomeDir
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	mode := permission.Mode(options.PermissionMode)
	if mode == "" {
		mode = permission.ModeDefault
	}
	cwd := options.WorkingDir
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	if options.Permissions == nil {
		options.Permissions = permission.New(mode, cwd, home)
	}
	if options.Tools == nil {
		options.Tools = tools.DefaultRegistry(cwd)
	}
	if options.Context == nil {
		options.Context = contextmgr.New(options.Model)
	}
	if options.SessionID == "" {
		options.SessionID = newSessionID()
	}
	result := &Agent{
		model: options.Model, client: options.Client, tools: options.Tools, permissions: options.Permissions,
		system: options.SystemPrompt, output: output, confirm: options.Confirm, thinking: options.Thinking,
		maxCostUSD: options.MaxCostUSD, maxTurns: options.MaxTurns, maxTokens: maxTokens, homeDir: home,
		workingDir: cwd, context: options.Context, sessions: options.SessionStore,
		sessionID: options.SessionID, sessionStart: time.Now().UTC(),
		events:     options.Events,
		baseSystem: options.SystemPrompt, memories: options.Memories, skills: options.Skills,
		subagents: options.SubAgents, planApproval: options.PlanApproval,
		surfacedMemories: map[string]bool{},
	}
	if mode == permission.ModePlan {
		result.prePlanMode = permission.ModeDefault
		_ = result.enterPlanMode()
	}
	return result
}

// Chat 使用后台 Context 执行一轮用户请求，供简单 CLI 调用。
func (a *Agent) Chat(input string) error {
	ctx, cancel := context.WithCancel(context.Background())
	a.processMu.Lock()
	if a.currentCancel != nil {
		a.processMu.Unlock()
		cancel()
		return errors.New("agent is already processing a request")
	}
	a.currentCancel = cancel
	a.processMu.Unlock()
	defer func() {
		cancel()
		a.processMu.Lock()
		a.currentCancel = nil
		a.processMu.Unlock()
	}()
	return a.ChatContext(ctx, input)
}

// ChatContext 执行统一 Agent Loop，直到模型不再调用工具或预算耗尽。
func (a *Agent) ChatContext(ctx context.Context, input string) error {
	if a.client == nil {
		return errors.New("LLM client is not configured")
	}
	userContent := input
	if a.memories != nil {
		prefetch := a.memories.StartPrefetch(ctx, input, a.client, a.model, a.surfacedMemories, a.sessionMemoryBytes)
		if prefetch != nil {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case result := <-prefetch.Done:
				// 记忆召回失败不能阻断主任务；成功结果作为系统提醒放在本轮用户文本前。
				if result.Err == nil && len(result.Memories) > 0 {
					remaining := memory.MaxSessionMemoryBytes - a.sessionMemoryBytes
					selected := make([]memory.Relevant, 0, len(result.Memories))
					for _, item := range result.Memories {
						if len(memory.FormatInjection(append(selected, item))) > remaining {
							break
						}
						selected = append(selected, item)
					}
					injection := memory.FormatInjection(selected)
					if injection == "" {
						break
					}
					userContent = injection + "\n\n" + input
					a.sessionMemoryBytes += len(injection)
					for _, item := range selected {
						a.surfacedMemories[item.Path] = true
					}
				}
			}
		}
	}
	a.mu.Lock()
	a.messages = append(a.messages, llm.TextMessage(llm.RoleUser, userContent))
	a.mu.Unlock()
	defer func() { _ = a.SaveSession() }()

	// 自动摘要只在普通用户消息刚追加后执行，避免切断 tool_call/tool_result 配对。
	a.mu.RLock()
	needsCompact := a.context.NeedsCompact(a.lastInputTokens)
	a.mu.RUnlock()
	if needsCompact {
		fmt.Fprintln(a.output, "Context window filling up, compacting conversation...")
		if err := a.compactContext(ctx); err != nil {
			return err
		}
	}

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if reason := a.budgetExceeded(); reason != "" {
			return errors.New(reason)
		}

		a.mu.Lock()
		a.messages = a.context.Prepare(a.messages, a.lastInputTokens)
		request := llm.Request{
			Model: a.model, System: a.system, Messages: append([]llm.Message(nil), a.messages...),
			Tools: a.tools.Definitions(), MaxTokens: a.maxTokens, Thinking: a.thinking,
		}
		a.mu.Unlock()

		response, err := a.streamWithRetry(ctx, request)
		if err != nil {
			return err
		}
		a.mu.Lock()
		a.messages = append(a.messages, response.Message)
		a.inputTokens += response.Usage.InputTokens
		a.outputTokens += response.Usage.OutputTokens
		a.turns++
		a.lastInputTokens = response.Usage.InputTokens
		a.mu.Unlock()
		a.context.MarkAPICall()

		if len(response.ToolCalls) == 0 {
			if response.Text != "" {
				fmt.Fprintln(a.output)
			}
			return nil
		}

		results := a.executeToolCalls(ctx, response.ToolCalls)
		blocks := make([]llm.ContentBlock, 0, len(results))
		for _, result := range results {
			blocks = append(blocks, llm.ContentBlock{
				Type: llm.BlockToolResult, ToolCallID: result.call.ID, ToolName: result.call.Name,
				Text: result.content, IsError: result.err != nil,
			})
		}
		a.mu.Lock()
		if a.pendingReset != nil {
			a.messages = a.pendingReset
			a.pendingReset = nil
			if a.pendingClearStats {
				a.inputTokens, a.outputTokens, a.lastInputTokens, a.turns = 0, 0, 0, 0
				a.pendingClearStats = false
			}
		} else {
			a.messages = append(a.messages, llm.Message{Role: llm.RoleTool, Blocks: blocks})
		}
		a.mu.Unlock()
	}
}

// Abort 取消当前由 Chat 启动的模型请求或工具执行。
func (a *Agent) Abort() {
	a.processMu.Lock()
	cancel := a.currentCancel
	a.processMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// IsProcessing 报告 Agent 当前是否正在处理 REPL 请求。
func (a *Agent) IsProcessing() bool {
	a.processMu.Lock()
	defer a.processMu.Unlock()
	return a.currentCancel != nil
}

// SetConfirm 设置交互确认函数；REPL 用同一个输入读取器调用它，避免抢占标准输入。
func (a *Agent) SetConfirm(confirm func(context.Context, string) (bool, error)) {
	a.confirm = ConfirmFunc(confirm)
}

// SetPlanApproval 设置 REPL 复用输入流的计划审批函数。
func (a *Agent) SetPlanApproval(approval func(context.Context, string) (PlanDecision, error)) {
	a.planApproval = PlanApprovalFunc(approval)
}

type toolExecution struct {
	call    llm.ToolCall
	content string
	err     error
}

func (a *Agent) executeToolCalls(ctx context.Context, calls []llm.ToolCall) []toolExecution {
	results := make([]toolExecution, len(calls))
	allSafe := true
	for _, call := range calls {
		allSafe = allSafe && a.tools.IsConcurrencySafe(call.Name)
	}
	if allSafe && len(calls) > 1 {
		group, groupCtx := errgroup.WithContext(ctx)
		for index := range calls {
			index := index
			group.Go(func() error {
				results[index] = a.executeToolCall(groupCtx, calls[index])
				return nil
			})
		}
		_ = group.Wait()
		return results
	}
	for index, call := range calls {
		results[index] = a.executeToolCall(ctx, call)
	}
	return results
}

func (a *Agent) executeToolCall(ctx context.Context, call llm.ToolCall) toolExecution {
	result := toolExecution{call: call}
	var input map[string]any
	if err := json.Unmarshal(call.Arguments, &input); err != nil {
		result.err = fmt.Errorf("invalid tool arguments: %w", err)
		result.content = result.err.Error()
		return result
	}
	decision := a.permissions.Check(call.Name, input)
	switch decision.Action {
	case permission.ActionDeny:
		result.err = errors.New(decision.Message)
		result.content = result.err.Error()
		return result
	case permission.ActionConfirm:
		if a.confirm == nil {
			result.err = errors.New("confirmation required but no interactive confirmer is available")
			result.content = result.err.Error()
			return result
		}
		allowed, err := a.confirm(ctx, decision.Message)
		if err != nil || !allowed {
			if err == nil {
				err = errors.New("user denied tool execution")
			}
			result.err, result.content = err, err.Error()
			return result
		}
	}

	if a.events != nil {
		a.events(Event{Type: EventToolStart, ToolName: call.Name, Arguments: call.Arguments})
	} else {
		a.outputMu.Lock()
		fmt.Fprintf(a.output, "\n  [tool] %s\n", call.Name)
		a.outputMu.Unlock()
	}
	var content string
	var err error
	switch call.Name {
	case "enter_plan_mode", "exit_plan_mode":
		content, err = a.executePlanTool(ctx, call.Name)
	case "skill":
		content, err = a.executeSkillTool(ctx, input)
	case "agent":
		content, err = a.executeSubAgentTool(ctx, input)
	default:
		content, err = a.tools.Execute(ctx, call)
	}
	if err == nil && a.memories != nil && (call.Name == "write_file" || call.Name == "edit_file") {
		if path, _ := input["file_path"].(string); a.memories.Contains(path) {
			if indexErr := a.memories.RefreshIndex(); indexErr != nil {
				err = indexErr
			}
		}
	}
	if err != nil {
		result.err = err
		content = "tool error: " + err.Error()
	}
	result.content = a.persistLargeResult(call.Name, content)
	if a.events != nil {
		a.events(Event{Type: EventToolResult, ToolName: call.Name, Result: result.content, IsError: result.err != nil})
	}
	return result
}

func (a *Agent) streamWithRetry(ctx context.Context, request llm.Request) (llm.Response, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if a.events != nil {
			a.events(Event{Type: EventModelStart})
		}
		emitted := false
		response, err := a.client.Stream(ctx, request, func(event llm.StreamEvent) error {
			if !emitted && a.events != nil {
				a.events(Event{Type: EventModelOutput})
			}
			emitted = true
			switch event.Type {
			case llm.EventTextDelta:
				_, err := io.WriteString(a.output, event.Text)
				return err
			case llm.EventThinkingDelta:
				_, err := io.WriteString(a.output, event.Text)
				return err
			}
			return nil
		})
		if !emitted && a.events != nil {
			a.events(Event{Type: EventModelOutput})
		}
		if err == nil {
			return response, nil
		}
		lastErr = err
		if emitted || !retryable(err) || attempt == 2 {
			break
		}
		delay := time.Duration(1<<attempt) * time.Second
		select {
		case <-ctx.Done():
			return llm.Response{}, ctx.Err()
		case <-time.After(delay):
		}
	}
	return llm.Response{}, lastErr
}

func retryable(err error) bool {
	var networkError net.Error
	if errors.As(err, &networkError) {
		return true
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "429") || strings.Contains(text, "503") || strings.Contains(text, "529") || strings.Contains(text, "overloaded")
}

func (a *Agent) persistLargeResult(toolName, result string) string {
	if len(result) <= largeResultLimit {
		return result
	}
	dir := filepath.Join(a.homeDir, ".mini-claude", "tool-results")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return tools.TruncateResult(result)
	}
	name := fmt.Sprintf("%d-%s.txt", time.Now().UnixNano(), safeFilename(toolName))
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(result), 0o600); err != nil {
		return tools.TruncateResult(result)
	}
	lines := strings.Split(result, "\n")
	previewCount := largeResultPreview
	if len(lines) < previewCount {
		previewCount = len(lines)
	}
	return tools.TruncateResult(fmt.Sprintf(
		"[Result too large (%.1f KB, %d lines). Full output saved to %s.]\n\nPreview (first %d lines):\n%s",
		float64(len(result))/1024, len(lines), path, previewCount, strings.Join(lines[:previewCount], "\n"),
	))
}

func safeFilename(value string) string {
	value = regexpUnsafeFilename.Replace(value)
	if value == "" {
		return "tool"
	}
	return value
}

var regexpUnsafeFilename = strings.NewReplacer("/", "_", "\\", "_", ":", "_")

func (a *Agent) executeSkillTool(ctx context.Context, input map[string]any) (string, error) {
	if a.skills == nil {
		return "", errors.New("skills registry is not configured")
	}
	name, _ := input["skill_name"].(string)
	arguments, _ := input["args"].(string)
	skill, ok := a.skills.Get(name)
	if !ok {
		return "", fmt.Errorf("unknown skill: %s", name)
	}
	prompt := skills.Resolve(skill, arguments)
	if skill.Context == skills.ContextFork {
		config := subagent.Config{Name: "skill-fork", Description: skill.Name, SystemPrompt: prompt, AllowedTools: skill.AllowedTools}
		return a.runSubAgent(ctx, config, firstNonEmpty(arguments, "Execute this skill task."))
	}
	return fmt.Sprintf("[Skill %q activated]\n\n%s", skill.Name, prompt), nil
}

func (a *Agent) executeSubAgentTool(ctx context.Context, input map[string]any) (string, error) {
	if a.subagents == nil {
		return "", errors.New("sub-agent registry is not configured")
	}
	agentType, _ := input["type"].(string)
	prompt, _ := input["prompt"].(string)
	if prompt == "" {
		return "", errors.New("sub-agent prompt is required")
	}
	return a.runSubAgent(ctx, a.subagents.Resolve(firstNonEmpty(agentType, "general")), prompt)
}

func (a *Agent) runSubAgent(ctx context.Context, config subagent.Config, prompt string) (string, error) {
	if a.events != nil {
		a.events(Event{Type: EventSubAgentStart, ToolName: config.Name, Result: config.Description})
		defer a.events(Event{Type: EventSubAgentEnd, ToolName: config.Name})
	}
	registry := a.tools.Subset(config.AllowedTools, "agent")
	var output bytes.Buffer
	maxTurns := a.maxTurns
	if maxTurns <= 0 {
		maxTurns = 20
	}
	child := New(Options{
		Model: a.model, Client: a.client, Tools: registry, Permissions: a.permissions, Confirm: a.confirm,
		SystemPrompt: config.SystemPrompt, Output: &output, Thinking: a.thinking, MaxTurns: maxTurns,
		MaxTokens: a.maxTokens, HomeDir: a.homeDir, WorkingDir: a.workingDir,
	})
	if err := child.ChatContext(ctx, prompt); err != nil {
		return "", err
	}
	usage := child.TokenUsage()
	a.mu.Lock()
	a.inputTokens += usage.InputTokens
	a.outputTokens += usage.OutputTokens
	a.mu.Unlock()
	text := strings.TrimSpace(output.String())
	if text == "" {
		text = "(Sub-agent produced no text output.)"
	}
	return text, nil
}

func (a *Agent) executePlanTool(ctx context.Context, name string) (string, error) {
	if name == "enter_plan_mode" {
		if a.permissions.Mode == permission.ModePlan {
			return "Already in plan mode.", nil
		}
		a.prePlanMode = a.permissions.Mode
		if err := a.enterPlanMode(); err != nil {
			return "", err
		}
		return "Entered plan mode. Explore read-only, write the plan file, then call exit_plan_mode.", nil
	}
	if a.permissions.Mode != permission.ModePlan {
		return "", errors.New("not in plan mode")
	}
	if a.planApproval == nil {
		return "", errors.New("plan approval requires an interactive callback")
	}
	content, err := os.ReadFile(a.planFilePath)
	if err != nil {
		return "", fmt.Errorf("read plan file: %w", err)
	}
	decision, err := a.planApproval(ctx, string(content))
	if err != nil {
		return "", err
	}
	if decision.Choice == PlanKeepPlanning {
		feedback := firstNonEmpty(decision.Feedback, "Please revise the plan.")
		return "User requested plan changes. Feedback: " + feedback, nil
	}
	if decision.Choice != PlanClearAndExecute && decision.Choice != PlanExecute && decision.Choice != PlanManualExecute {
		return "", fmt.Errorf("invalid plan approval choice: %q", decision.Choice)
	}
	target := a.prePlanMode
	if target == "" || target == permission.ModePlan {
		target = permission.ModeDefault
	}
	if decision.Choice == PlanExecute || decision.Choice == PlanClearAndExecute {
		target = permission.ModeAcceptEdits
	}
	a.leavePlanMode(target)
	if decision.Choice == PlanClearAndExecute {
		a.mu.Lock()
		a.pendingReset = []llm.Message{llm.TextMessage(llm.RoleUser, "Execute this approved implementation plan:\n\n"+string(content))}
		a.pendingClearStats = true
		a.mu.Unlock()
	}
	return fmt.Sprintf("Plan approved. Continue execution in %s mode.", target), nil
}

func (a *Agent) enterPlanMode() error {
	dir := filepath.Join(a.homeDir, ".claude", "plans")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if a.planFilePath == "" {
		a.planFilePath = filepath.Join(dir, "plan-"+a.sessionID+".md")
	}
	a.permissions.Mode = permission.ModePlan
	a.permissions.PlanFilePath = a.planFilePath
	a.system = a.baseSystem + fmt.Sprintf(`

# Plan Mode Active
You are in read-only plan mode. The only writable file is: %s
Explore the project, write a structured implementation plan to that file, then call exit_plan_mode for approval.`, a.planFilePath)
	a.tools.Activate("enter_plan_mode", "exit_plan_mode")
	return nil
}

func (a *Agent) leavePlanMode(mode permission.Mode) {
	a.permissions.Mode = mode
	a.permissions.PlanFilePath = ""
	a.system = a.baseSystem
	a.prePlanMode = ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// ClearHistory 清空消息、token 和轮次统计。
func (a *Agent) ClearHistory() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.messages = nil
	a.inputTokens, a.outputTokens, a.lastInputTokens, a.turns = 0, 0, 0, 0
	fmt.Fprintln(a.output, "Conversation cleared.")
}

// Compact 手动调用模型把旧历史压缩成摘要。
func (a *Agent) Compact() error {
	return a.compactContext(context.Background())
}

func (a *Agent) compactContext(ctx context.Context) error {
	a.mu.RLock()
	messages := append([]llm.Message(nil), a.messages...)
	a.mu.RUnlock()
	compacted, usage, changed, err := a.context.Compact(ctx, a.client, a.model, a.system, messages)
	if err != nil {
		return err
	}
	if !changed {
		fmt.Fprintln(a.output, "Conversation is too short to compact.")
		return nil
	}
	a.mu.Lock()
	a.messages = compacted
	a.inputTokens += usage.InputTokens
	a.outputTokens += usage.OutputTokens
	a.lastInputTokens = 0
	a.mu.Unlock()
	fmt.Fprintln(a.output, "Conversation compacted.")
	return a.SaveSession()
}

// TogglePlanMode 在普通模式和只读计划模式之间切换。
func (a *Agent) TogglePlanMode() string {
	if a.permissions.Mode == permission.ModePlan {
		mode := a.prePlanMode
		if mode == "" {
			mode = permission.ModeDefault
		}
		a.leavePlanMode(mode)
		return string(mode)
	}
	a.prePlanMode = a.permissions.Mode
	if err := a.enterPlanMode(); err != nil {
		return "error: " + err.Error()
	}
	return string(permission.ModePlan)
}

// PlanFilePath 返回当前计划文件路径。
func (a *Agent) PlanFilePath() string { return a.planFilePath }

// MemoryDescriptions 返回 REPL 可展示的记忆摘要。
func (a *Agent) MemoryDescriptions() []string {
	if a.memories == nil {
		return nil
	}
	items, _ := a.memories.List()
	result := make([]string, 0, len(items))
	for _, item := range items {
		result = append(result, fmt.Sprintf("[%s] %s — %s", item.Type, item.Name, item.Description))
	}
	return result
}

// SkillDescriptions 返回 REPL 可展示的技能摘要。
func (a *Agent) SkillDescriptions() []string {
	if a.skills == nil {
		return nil
	}
	items := a.skills.Discover()
	result := make([]string, 0, len(items))
	for _, item := range items {
		name := item.Name
		if item.UserInvocable {
			name = "/" + name
		}
		result = append(result, fmt.Sprintf("%s (%s) — %s", name, item.Source, item.Description))
	}
	return result
}

// InvokeSkill 处理 REPL 的 /<skill> 调用；返回 false 表示不是已注册技能。
func (a *Agent) InvokeSkill(command string) (bool, error) {
	if a.skills == nil || !strings.HasPrefix(command, "/") {
		return false, nil
	}
	parts := strings.SplitN(strings.TrimPrefix(command, "/"), " ", 2)
	skill, ok := a.skills.Get(parts[0])
	if !ok || !skill.UserInvocable {
		return false, nil
	}
	arguments := ""
	if len(parts) == 2 {
		arguments = parts[1]
	}
	prompt := skills.Resolve(skill, arguments)
	if skill.Context == skills.ContextFork {
		result, err := a.runSubAgent(context.Background(), subagent.Config{Name: "skill-fork", SystemPrompt: prompt, AllowedTools: skill.AllowedTools}, firstNonEmpty(arguments, "Execute this skill task."))
		if result != "" {
			fmt.Fprintln(a.output, result)
		}
		return true, err
	}
	return true, a.Chat(prompt)
}

// RestoreSession 用版本化会话恢复消息和 token 统计。
func (a *Agent) RestoreSession(data session.Data) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.messages = append([]llm.Message(nil), data.Messages...)
	a.inputTokens, a.outputTokens = data.Usage.InputTokens, data.Usage.OutputTokens
	a.sessionID = data.Metadata.ID
	if !data.Metadata.StartTime.IsZero() {
		a.sessionStart = data.Metadata.StartTime
	}
}

// SaveSession 自动保存当前中立历史；未配置存储时为空操作。
func (a *Agent) SaveSession() error {
	if a.sessions == nil {
		return nil
	}
	a.mu.RLock()
	data := session.Data{
		Metadata: session.Metadata{ID: a.sessionID, Model: a.model, CWD: a.workingDir, StartTime: a.sessionStart},
		Messages: append([]llm.Message(nil), a.messages...),
		Usage:    llm.Usage{InputTokens: a.inputTokens, OutputTokens: a.outputTokens},
	}
	a.mu.RUnlock()
	return a.sessions.Save(data)
}

// ShowCost 输出累计 token、估算费用和轮次。
func (a *Agent) ShowCost() {
	a.mu.RLock()
	defer a.mu.RUnlock()
	fmt.Fprintf(a.output, "Tokens: %d in / %d out\nEstimated cost: $%.4f | Turns: %d\n",
		a.inputTokens, a.outputTokens, costUSD(a.inputTokens, a.outputTokens), a.turns)
}

// Messages 返回消息历史的只读副本。
func (a *Agent) Messages() []llm.Message {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return append([]llm.Message(nil), a.messages...)
}

// TokenUsage 返回累计 token 使用量。
func (a *Agent) TokenUsage() llm.Usage {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return llm.Usage{InputTokens: a.inputTokens, OutputTokens: a.outputTokens}
}

func (a *Agent) budgetExceeded() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.maxTurns > 0 && a.turns >= a.maxTurns {
		return fmt.Sprintf("turn limit reached (%d >= %d)", a.turns, a.maxTurns)
	}
	if a.maxCostUSD > 0 && costUSD(a.inputTokens, a.outputTokens) >= a.maxCostUSD {
		return fmt.Sprintf("cost limit reached ($%.4f >= $%.4f)", costUSD(a.inputTokens, a.outputTokens), a.maxCostUSD)
	}
	return ""
}

func costUSD(input, output int64) float64 {
	return float64(input)/1_000_000*3 + float64(output)/1_000_000*15
}

func newSessionID() string {
	var value [4]byte
	if _, err := rand.Read(value[:]); err == nil {
		return fmt.Sprintf("%x", value[:])
	}
	return fmt.Sprintf("%x", time.Now().UnixNano())
}
