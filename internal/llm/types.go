package llm

import (
	"context"
	"encoding/json"
)

// Role 表示一条消息在对话中的身份。
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// BlockType 表示消息内容块的种类。
type BlockType string

const (
	BlockText       BlockType = "text"
	BlockThinking   BlockType = "thinking"
	BlockToolCall   BlockType = "tool_call"
	BlockToolResult BlockType = "tool_result"
)

// ContentBlock 是与模型厂商无关的消息内容。
type ContentBlock struct {
	Type       BlockType       `json:"type"`
	Text       string          `json:"text,omitempty"`
	Signature  string          `json:"signature,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	ToolName   string          `json:"tool_name,omitempty"`
	Arguments  json.RawMessage `json:"arguments,omitempty"`
	IsError    bool            `json:"is_error,omitempty"`
}

// Message 是 Agent 保存和传递的中立消息格式。
type Message struct {
	Role   Role           `json:"role"`
	Blocks []ContentBlock `json:"blocks"`
}

// ToolDefinition 描述模型可以调用的工具及其 JSON Schema。
type ToolDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
	Deferred    bool           `json:"deferred,omitempty"`
}

// ToolCall 是模型生成的一次工具调用。
type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// Usage 记录一次请求的 token 消耗。
type Usage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
}

// Request 是发送给任意模型后端的统一请求。
type Request struct {
	Model       string           `json:"model"`
	System      string           `json:"system,omitempty"`
	Messages    []Message        `json:"messages"`
	Tools       []ToolDefinition `json:"tools,omitempty"`
	MaxTokens   int64            `json:"max_tokens,omitempty"`
	Thinking    bool             `json:"thinking,omitempty"`
	Temperature *float64         `json:"temperature,omitempty"`
}

// Response 是一次完整模型响应。
type Response struct {
	Message    Message    `json:"message"`
	Text       string     `json:"text,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	Usage      Usage      `json:"usage"`
	StopReason string     `json:"stop_reason,omitempty"`
}

// StreamEventType 表示流式回调的事件类型。
type StreamEventType string

const (
	EventTextDelta     StreamEventType = "text_delta"
	EventThinkingDelta StreamEventType = "thinking_delta"
	EventToolCall      StreamEventType = "tool_call"
)

// StreamEvent 是模型流式返回给 UI 的增量事件。
type StreamEvent struct {
	Type     StreamEventType `json:"type"`
	Text     string          `json:"text,omitempty"`
	ToolCall *ToolCall       `json:"tool_call,omitempty"`
}

// EmitFunc 接收模型增量；返回错误会终止流读取。
type EmitFunc func(StreamEvent) error

// Client 隔离具体模型 SDK，使 Agent Loop 能复用同一套逻辑。
type Client interface {
	Complete(ctx context.Context, request Request) (Response, error)
	Stream(ctx context.Context, request Request, emit EmitFunc) (Response, error)
}

// TextMessage 创建只包含文本的消息。
func TextMessage(role Role, text string) Message {
	return Message{Role: role, Blocks: []ContentBlock{{Type: BlockText, Text: text}}}
}

// ToolResultMessage 创建工具结果消息。
func ToolResultMessage(callID, name, content string, isError bool) Message {
	return Message{Role: RoleTool, Blocks: []ContentBlock{{
		Type: BlockToolResult, ToolCallID: callID, ToolName: name, Text: content, IsError: isError,
	}}}
}
