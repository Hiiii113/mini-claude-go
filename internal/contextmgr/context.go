// Package contextmgr 实现无需区分模型厂商的上下文裁剪与摘要压缩。
package contextmgr

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"mini-claude/internal/llm"
)

const (
	snipPlaceholder    = "[Content snipped - re-read if needed]"
	clearedPlaceholder = "[Old result cleared]"
	keepRecentResults  = 3
	microcompactIdle   = 5 * time.Minute
)

// Manager 保存模型上下文窗口和最近调用时间，并执行四层压缩策略。
type Manager struct {
	window int64
	now    func() time.Time

	mu          sync.Mutex
	lastAPICall time.Time
}

// New 根据模型名选择保守的有效上下文窗口。
func New(model string) *Manager {
	window := int64(200_000)
	lower := strings.ToLower(model)
	if strings.Contains(lower, "gpt-4o") {
		window = 128_000
	}
	// 预留输出和协议开销，避免刚好顶到厂商硬限制。
	if window > 20_000 {
		window -= 20_000
	}
	return &Manager{window: window, now: time.Now}
}

// NewWithWindow 创建指定窗口的管理器，主要用于确定性测试。
func NewWithWindow(window int64) *Manager {
	return &Manager{window: window, now: time.Now}
}

// Prepare 在发送请求前依次执行结果预算裁剪、旧结果 snip 和空闲 microcompact。
func (m *Manager) Prepare(messages []llm.Message, inputTokens int64) []llm.Message {
	result := cloneMessages(messages)
	if m.window <= 0 {
		return result
	}
	utilization := float64(inputTokens) / float64(m.window)
	if utilization >= 0.5 {
		budget := 30_000
		if utilization > 0.7 {
			budget = 15_000
		}
		budgetResults(result, budget)
	}
	if utilization >= 0.6 {
		snipStaleResults(result)
	}
	m.mu.Lock()
	idle := !m.lastAPICall.IsZero() && m.now().Sub(m.lastAPICall) >= microcompactIdle
	m.mu.Unlock()
	if idle {
		microcompact(result)
	}
	return result
}

// MarkAPICall 记录一次模型请求完成的时间，供空闲压缩判断。
func (m *Manager) MarkAPICall() {
	m.mu.Lock()
	m.lastAPICall = m.now()
	m.mu.Unlock()
}

// NeedsCompact 判断上一请求是否达到 85% 自动摘要阈值。
func (m *Manager) NeedsCompact(inputTokens int64) bool {
	return m.window > 0 && float64(inputTokens) >= float64(m.window)*0.85
}

// Compact 调用模型总结旧历史，同时保留最后一条普通用户消息。
func (m *Manager) Compact(ctx context.Context, client llm.Client, model, system string, messages []llm.Message) ([]llm.Message, llm.Usage, bool, error) {
	if len(messages) < 4 {
		return cloneMessages(messages), llm.Usage{}, false, nil
	}
	last := messages[len(messages)-1]
	history := messages
	keepLast := last.Role == llm.RoleUser && onlyPlainText(last)
	if keepLast {
		history = messages[:len(messages)-1]
	}
	requestMessages := append(cloneMessages(history), llm.TextMessage(llm.RoleUser,
		"Summarize the conversation so far. Preserve decisions, file paths, errors, pending work, and facts needed to continue."))
	response, err := client.Complete(ctx, llm.Request{
		Model: model, System: "You summarize coding conversations concisely and accurately.",
		Messages: requestMessages, MaxTokens: 2048,
	})
	if err != nil {
		return nil, llm.Usage{}, false, fmt.Errorf("compact context: %w", err)
	}
	summary := strings.TrimSpace(response.Text)
	if summary == "" {
		return nil, response.Usage, false, fmt.Errorf("compact context: model returned an empty summary")
	}
	result := []llm.Message{
		llm.TextMessage(llm.RoleUser, "[Previous conversation summary]\n"+summary),
		llm.TextMessage(llm.RoleAssistant, "Understood. I will continue from this summary."),
	}
	if keepLast {
		result = append(result, cloneMessages([]llm.Message{last})[0])
	}
	_ = system // 保留参数以便未来摘要器加入项目系统约束，当前避免把完整工具说明重复计费。
	return result, response.Usage, true, nil
}

func budgetResults(messages []llm.Message, budget int) {
	for messageIndex := range messages {
		for blockIndex := range messages[messageIndex].Blocks {
			block := &messages[messageIndex].Blocks[blockIndex]
			if block.Type != llm.BlockToolResult || len(block.Text) <= budget {
				continue
			}
			keep := (budget - 100) / 2
			omitted := len(block.Text) - keep*2
			block.Text = block.Text[:keep] + fmt.Sprintf("\n\n[... budgeted: %d chars truncated ...]\n\n", omitted) + block.Text[len(block.Text)-keep:]
		}
	}
}

func snipStaleResults(messages []llm.Message) {
	type location struct{ message, block int }
	var results []location
	for messageIndex := range messages {
		for blockIndex, block := range messages[messageIndex].Blocks {
			if block.Type == llm.BlockToolResult && isSnippable(block.ToolName) && block.Text != snipPlaceholder {
				results = append(results, location{messageIndex, blockIndex})
			}
		}
	}
	for index := 0; index < len(results)-keepRecentResults; index++ {
		location := results[index]
		messages[location.message].Blocks[location.block].Text = snipPlaceholder
	}
}

func microcompact(messages []llm.Message) {
	type location struct{ message, block int }
	var results []location
	for messageIndex := range messages {
		for blockIndex, block := range messages[messageIndex].Blocks {
			if block.Type == llm.BlockToolResult && block.Text != snipPlaceholder && block.Text != clearedPlaceholder {
				results = append(results, location{messageIndex, blockIndex})
			}
		}
	}
	for index := 0; index < len(results)-keepRecentResults; index++ {
		location := results[index]
		messages[location.message].Blocks[location.block].Text = clearedPlaceholder
	}
}

func isSnippable(name string) bool {
	switch name {
	case "read_file", "grep_search", "list_files", "run_shell":
		return true
	default:
		return false
	}
}

func onlyPlainText(message llm.Message) bool {
	if len(message.Blocks) == 0 {
		return false
	}
	for _, block := range message.Blocks {
		if block.Type != llm.BlockText {
			return false
		}
	}
	return true
}

func cloneMessages(messages []llm.Message) []llm.Message {
	result := make([]llm.Message, len(messages))
	for index, message := range messages {
		result[index] = message
		result[index].Blocks = append([]llm.ContentBlock(nil), message.Blocks...)
		for blockIndex := range result[index].Blocks {
			result[index].Blocks[blockIndex].Arguments = append([]byte(nil), result[index].Blocks[blockIndex].Arguments...)
		}
	}
	return result
}
