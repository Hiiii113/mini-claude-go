package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// AnthropicOptions 配置 Anthropic 原生客户端。
type AnthropicOptions struct {
	APIKey  string
	BaseURL string
}

// AnthropicClient 将统一消息协议转换成 Anthropic Messages 协议。
type AnthropicClient struct {
	client anthropic.Client
}

// NewAnthropicClient 创建支持代理 Base URL 的 Anthropic 客户端。
func NewAnthropicClient(options AnthropicOptions) *AnthropicClient {
	opts := []option.RequestOption{option.WithAPIKey(options.APIKey)}
	if options.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(strings.TrimRight(options.BaseURL, "/")))
	}
	return &AnthropicClient{client: anthropic.NewClient(opts...)}
}

// Complete 执行一次非流式 Anthropic 请求。
func (c *AnthropicClient) Complete(ctx context.Context, request Request) (Response, error) {
	params, err := buildAnthropicParams(request)
	if err != nil {
		return Response{}, err
	}
	message, err := c.client.Messages.New(ctx, params)
	if err != nil {
		return Response{}, fmt.Errorf("anthropic completion: %w", err)
	}
	return fromAnthropicMessage(*message), nil
}

// Stream 读取 Anthropic SSE，并同时累积可保存的完整消息。
func (c *AnthropicClient) Stream(ctx context.Context, request Request, emit EmitFunc) (Response, error) {
	params, err := buildAnthropicParams(request)
	if err != nil {
		return Response{}, err
	}
	stream := c.client.Messages.NewStreaming(ctx, params)
	defer stream.Close()

	var accumulated anthropic.Message
	for stream.Next() {
		event := stream.Current()
		if err := accumulated.Accumulate(event); err != nil {
			return Response{}, fmt.Errorf("anthropic stream accumulate: %w", err)
		}
		if emit == nil || event.Type != "content_block_delta" {
			continue
		}
		switch event.Delta.Type {
		case "text_delta":
			if err := emit(StreamEvent{Type: EventTextDelta, Text: event.Delta.Text}); err != nil {
				return Response{}, err
			}
		case "thinking_delta":
			if err := emit(StreamEvent{Type: EventThinkingDelta, Text: event.Delta.Thinking}); err != nil {
				return Response{}, err
			}
		}
	}
	if err := stream.Err(); err != nil {
		return Response{}, fmt.Errorf("anthropic stream: %w", err)
	}
	response := fromAnthropicMessage(accumulated)
	if emit != nil {
		for i := range response.ToolCalls {
			call := response.ToolCalls[i]
			if err := emit(StreamEvent{Type: EventToolCall, ToolCall: &call}); err != nil {
				return Response{}, err
			}
		}
	}
	return response, nil
}

func buildAnthropicParams(request Request) (anthropic.MessageNewParams, error) {
	messages := make([]anthropic.MessageParam, 0, len(request.Messages))
	for _, message := range request.Messages {
		if message.Role == RoleSystem {
			continue
		}
		converted, err := toAnthropicMessage(message)
		if err != nil {
			return anthropic.MessageNewParams{}, err
		}
		messages = append(messages, converted)
	}
	maxTokens := request.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 16384
	}
	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(request.Model),
		MaxTokens: maxTokens,
		Messages:  messages,
		Tools:     toAnthropicTools(request.Tools),
	}
	if request.System != "" {
		params.System = []anthropic.TextBlockParam{{Text: request.System}}
	}
	if request.Temperature != nil {
		params.Temperature = anthropic.Float(*request.Temperature)
	}
	if request.Thinking {
		budget := maxTokens - 1
		if budget < 1024 {
			budget = 1024
		}
		params.Thinking = anthropic.ThinkingConfigParamOfEnabled(budget)
	}
	return params, nil
}

func toAnthropicMessage(message Message) (anthropic.MessageParam, error) {
	blocks := make([]anthropic.ContentBlockParamUnion, 0, len(message.Blocks))
	for _, block := range message.Blocks {
		switch block.Type {
		case BlockText:
			blocks = append(blocks, anthropic.NewTextBlock(block.Text))
		case BlockThinking:
			if block.Signature != "" {
				blocks = append(blocks, anthropic.NewThinkingBlock(block.Signature, block.Text))
			}
		case BlockToolCall:
			var input any = map[string]any{}
			if len(block.Arguments) > 0 {
				if err := json.Unmarshal(block.Arguments, &input); err != nil {
					return anthropic.MessageParam{}, fmt.Errorf("anthropic tool arguments: %w", err)
				}
			}
			blocks = append(blocks, anthropic.NewToolUseBlock(block.ToolCallID, input, block.ToolName))
		case BlockToolResult:
			blocks = append(blocks, anthropic.NewToolResultBlock(block.ToolCallID, block.Text, block.IsError))
		}
	}

	if message.Role == RoleAssistant {
		return anthropic.NewAssistantMessage(blocks...), nil
	}
	if message.Role == RoleUser || message.Role == RoleTool {
		return anthropic.NewUserMessage(blocks...), nil
	}
	return anthropic.MessageParam{}, fmt.Errorf("anthropic conversion: unsupported role %q", message.Role)
}

func toAnthropicTools(definitions []ToolDefinition) []anthropic.ToolUnionParam {
	result := make([]anthropic.ToolUnionParam, 0, len(definitions))
	for _, definition := range definitions {
		schema := anthropic.ToolInputSchemaParam{ExtraFields: map[string]any{}}
		for key, value := range definition.InputSchema {
			switch key {
			case "properties":
				schema.Properties = value
			case "required":
				if items, ok := value.([]string); ok {
					schema.Required = items
				} else if items, ok := value.([]any); ok {
					for _, item := range items {
						if text, ok := item.(string); ok {
							schema.Required = append(schema.Required, text)
						}
					}
				}
			case "type":
				// SDK 会把零值序列化为 object。
			default:
				schema.ExtraFields[key] = value
			}
		}
		tool := anthropic.ToolParam{
			Name: definition.Name, Description: anthropic.String(definition.Description), InputSchema: schema,
			DeferLoading: anthropic.Bool(definition.Deferred),
		}
		result = append(result, anthropic.ToolUnionParam{OfTool: &tool})
	}
	return result
}

func fromAnthropicMessage(message anthropic.Message) Response {
	blocks := make([]ContentBlock, 0, len(message.Content))
	calls := make([]ToolCall, 0)
	var text strings.Builder
	for _, block := range message.Content {
		switch block.Type {
		case "text":
			blocks = append(blocks, ContentBlock{Type: BlockText, Text: block.Text})
			text.WriteString(block.Text)
		case "thinking":
			blocks = append(blocks, ContentBlock{Type: BlockThinking, Text: block.Thinking, Signature: block.Signature})
		case "tool_use":
			args := json.RawMessage(block.Input)
			call := ToolCall{ID: block.ID, Name: block.Name, Arguments: args}
			calls = append(calls, call)
			blocks = append(blocks, ContentBlock{
				Type: BlockToolCall, ToolCallID: block.ID, ToolName: block.Name, Arguments: args,
			})
		}
	}
	return Response{
		Message: Message{Role: RoleAssistant, Blocks: blocks}, Text: text.String(), ToolCalls: calls,
		Usage:      Usage{InputTokens: message.Usage.InputTokens, OutputTokens: message.Usage.OutputTokens},
		StopReason: string(message.StopReason),
	}
}
