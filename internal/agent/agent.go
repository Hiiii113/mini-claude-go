package agent

import (
	"context"
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

	"mini-claude/internal/llm"
	"mini-claude/internal/permission"
	"mini-claude/internal/tools"
)

const (
	defaultMaxTokens   = int64(16384)
	largeResultLimit   = 30 * 1024
	largeResultPreview = 200
)

// ConfirmFunc 在权限层要求确认时询问用户。
type ConfirmFunc func(ctx context.Context, message string) (bool, error)

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
}

// Agent 是与模型厂商无关的核心 Agent Loop。
type Agent struct {
	model       string
	client      llm.Client
	tools       *tools.Registry
	permissions *permission.Evaluator
	system      string
	output      io.Writer
	confirm     ConfirmFunc
	thinking    bool
	maxCostUSD  float64
	maxTurns    int
	maxTokens   int64
	homeDir     string

	mu           sync.RWMutex
	messages     []llm.Message
	inputTokens  int64
	outputTokens int64
	turns        int
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
	if options.Permissions == nil {
		cwd, _ := os.Getwd()
		options.Permissions = permission.New(mode, cwd, home)
	}
	if options.Tools == nil {
		cwd, _ := os.Getwd()
		options.Tools = tools.DefaultRegistry(cwd)
	}
	return &Agent{
		model: options.Model, client: options.Client, tools: options.Tools, permissions: options.Permissions,
		system: options.SystemPrompt, output: output, confirm: options.Confirm, thinking: options.Thinking,
		maxCostUSD: options.MaxCostUSD, maxTurns: options.MaxTurns, maxTokens: maxTokens, homeDir: home,
	}
}

// Chat 使用后台 Context 执行一轮用户请求，供简单 CLI 调用。
func (a *Agent) Chat(input string) error {
	return a.ChatContext(context.Background(), input)
}

// ChatContext 执行统一 Agent Loop，直到模型不再调用工具或预算耗尽。
func (a *Agent) ChatContext(ctx context.Context, input string) error {
	if a.client == nil {
		return errors.New("LLM client is not configured")
	}
	a.mu.Lock()
	a.messages = append(a.messages, llm.TextMessage(llm.RoleUser, input))
	a.mu.Unlock()

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if reason := a.budgetExceeded(); reason != "" {
			return errors.New(reason)
		}

		a.mu.RLock()
		request := llm.Request{
			Model: a.model, System: a.system, Messages: append([]llm.Message(nil), a.messages...),
			Tools: a.tools.Definitions(), MaxTokens: a.maxTokens, Thinking: a.thinking,
		}
		a.mu.RUnlock()

		response, err := a.streamWithRetry(ctx, request)
		if err != nil {
			return err
		}
		a.mu.Lock()
		a.messages = append(a.messages, response.Message)
		a.inputTokens += response.Usage.InputTokens
		a.outputTokens += response.Usage.OutputTokens
		a.turns++
		a.mu.Unlock()

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

	fmt.Fprintf(a.output, "\n  [tool] %s\n", call.Name)
	content, err := a.tools.Execute(ctx, call)
	if err != nil {
		result.err = err
		content = "tool error: " + err.Error()
	}
	result.content = a.persistLargeResult(call.Name, content)
	return result
}

func (a *Agent) streamWithRetry(ctx context.Context, request llm.Request) (llm.Response, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		emitted := false
		response, err := a.client.Stream(ctx, request, func(event llm.StreamEvent) error {
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
	a.inputTokens, a.outputTokens, a.turns = 0, 0, 0
	fmt.Fprintln(a.output, "Conversation cleared.")
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
