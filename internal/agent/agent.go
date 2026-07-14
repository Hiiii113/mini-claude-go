package agent

import (
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
	"mini-claude/internal/permission"
	"mini-claude/internal/session"
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
	EventModelStart  EventType = "model_start"
	EventModelOutput EventType = "model_output"
	EventToolStart   EventType = "tool_start"
	EventToolResult  EventType = "tool_result"
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
	messages        []llm.Message
	inputTokens     int64
	outputTokens    int64
	turns           int
	lastInputTokens int64

	processMu     sync.Mutex
	currentCancel context.CancelFunc
	prePlanMode   permission.Mode
	events        EventFunc
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
	return &Agent{
		model: options.Model, client: options.Client, tools: options.Tools, permissions: options.Permissions,
		system: options.SystemPrompt, output: output, confirm: options.Confirm, thinking: options.Thinking,
		maxCostUSD: options.MaxCostUSD, maxTurns: options.MaxTurns, maxTokens: maxTokens, homeDir: home,
		workingDir: cwd, context: options.Context, sessions: options.SessionStore,
		sessionID: options.SessionID, sessionStart: time.Now().UTC(),
		events: options.Events,
	}
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
	a.mu.Lock()
	a.messages = append(a.messages, llm.TextMessage(llm.RoleUser, input))
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
		a.messages = append(a.messages, llm.Message{Role: llm.RoleTool, Blocks: blocks})
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
		fmt.Fprintf(a.output, "\n  [tool] %s\n", call.Name)
	}
	content, err := a.tools.Execute(ctx, call)
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

// TogglePlanMode 在普通模式和只读计划模式之间切换；完整审批流程由阶段五接入。
func (a *Agent) TogglePlanMode() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.permissions.Mode == permission.ModePlan {
		mode := a.prePlanMode
		if mode == "" {
			mode = permission.ModeDefault
		}
		a.permissions.Mode = mode
		a.prePlanMode = ""
		return string(mode)
	}
	a.prePlanMode = a.permissions.Mode
	a.permissions.Mode = permission.ModePlan
	return string(permission.ModePlan)
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
