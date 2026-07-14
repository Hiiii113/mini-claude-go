package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/shared"
)

// OpenAIOptions 配置 OpenAI-compatible 客户端。
type OpenAIOptions struct {
	APIKey  string
	BaseURL string
}

// OpenAIClient 将统一消息协议转换成 Chat Completions 协议。
type OpenAIClient struct {
	client openai.Client
}

// NewOpenAIClient 创建支持自定义 Base URL 的 OpenAI-compatible 客户端。
func NewOpenAIClient(options OpenAIOptions) *OpenAIClient {
	opts := []option.RequestOption{option.WithAPIKey(options.APIKey)}
	if options.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(strings.TrimRight(options.BaseURL, "/")))
	}
	return &OpenAIClient{client: openai.NewClient(opts...)}
}

// Complete 执行一次非流式模型请求。
func (c *OpenAIClient) Complete(ctx context.Context, request Request) (Response, error) {
	params, err := buildOpenAIParams(request)
	if err != nil {
		return Response{}, err
	}
	completion, err := c.client.Chat.Completions.New(ctx, params)
	if err != nil {
		return Response{}, fmt.Errorf("openai completion: %w", err)
	}
	if len(completion.Choices) == 0 {
		return Response{}, fmt.Errorf("openai completion: empty choices")
	}

	message := completion.Choices[0].Message
	response := Response{
		Text:       message.Content,
		StopReason: completion.Choices[0].FinishReason,
		Usage: Usage{
			InputTokens:  completion.Usage.PromptTokens,
			OutputTokens: completion.Usage.CompletionTokens,
		},
	}
	response.ToolCalls = openAIToolCalls(message.ToolCalls)
	response.Message = assistantResponseMessage(response.Text, response.ToolCalls)
	return response, nil
}

// Stream 读取 SSE 增量并在结束时返回可持久化的完整响应。
func (c *OpenAIClient) Stream(ctx context.Context, request Request, emit EmitFunc) (Response, error) {
	params, err := buildOpenAIParams(request)
	if err != nil {
		return Response{}, err
	}
	stream := c.client.Chat.Completions.NewStreaming(ctx, params)
	defer stream.Close()

	var text strings.Builder
	type pendingCall struct {
		ID, Name  string
		Arguments strings.Builder
	}
	pending := map[int64]*pendingCall{}
	var usage Usage
	var stopReason string

	for stream.Next() {
		chunk := stream.Current()
		if chunk.Usage.PromptTokens > 0 || chunk.Usage.CompletionTokens > 0 {
			usage.InputTokens = chunk.Usage.PromptTokens
			usage.OutputTokens = chunk.Usage.CompletionTokens
		}
		for _, choice := range chunk.Choices {
			if choice.FinishReason != "" {
				stopReason = choice.FinishReason
			}
			if choice.Delta.Content != "" {
				text.WriteString(choice.Delta.Content)
				if emit != nil {
					if err := emit(StreamEvent{Type: EventTextDelta, Text: choice.Delta.Content}); err != nil {
						return Response{}, err
					}
				}
			}
			for _, delta := range choice.Delta.ToolCalls {
				call := pending[delta.Index]
				if call == nil {
					call = &pendingCall{}
					pending[delta.Index] = call
				}
				if delta.ID != "" {
					call.ID = delta.ID
				}
				if delta.Function.Name != "" {
					call.Name += delta.Function.Name
				}
				call.Arguments.WriteString(delta.Function.Arguments)
			}
		}
	}
	if err := stream.Err(); err != nil {
		return Response{}, fmt.Errorf("openai stream: %w", err)
	}

	indices := make([]int, 0, len(pending))
	for index := range pending {
		indices = append(indices, int(index))
	}
	sort.Ints(indices)
	calls := make([]ToolCall, 0, len(indices))
	for _, index := range indices {
		item := pending[int64(index)]
		args := json.RawMessage(item.Arguments.String())
		if !json.Valid(args) {
			args = json.RawMessage(`{}`)
		}
		call := ToolCall{ID: item.ID, Name: item.Name, Arguments: args}
		calls = append(calls, call)
		if emit != nil {
			if err := emit(StreamEvent{Type: EventToolCall, ToolCall: &call}); err != nil {
				return Response{}, err
			}
		}
	}

	response := Response{Text: text.String(), ToolCalls: calls, Usage: usage, StopReason: stopReason}
	response.Message = assistantResponseMessage(response.Text, response.ToolCalls)
	return response, nil
}

func buildOpenAIParams(request Request) (openai.ChatCompletionNewParams, error) {
	messages := make([]openai.ChatCompletionMessageParamUnion, 0, len(request.Messages)+1)
	if request.System != "" {
		messages = append(messages, openai.SystemMessage(request.System))
	}
	for _, message := range request.Messages {
		converted, err := toOpenAIMessages(message)
		if err != nil {
			return openai.ChatCompletionNewParams{}, err
		}
		messages = append(messages, converted...)
	}

	params := openai.ChatCompletionNewParams{
		Model:    shared.ChatModel(request.Model),
		Messages: messages,
		Tools:    toOpenAITools(request.Tools),
	}
	if request.MaxTokens > 0 {
		params.MaxTokens = openai.Int(request.MaxTokens)
	}
	if request.Temperature != nil {
		params.Temperature = openai.Float(*request.Temperature)
	}
	return params, nil
}

func toOpenAIMessages(message Message) ([]openai.ChatCompletionMessageParamUnion, error) {
	text := blocksText(message.Blocks)
	switch message.Role {
	case RoleSystem:
		return []openai.ChatCompletionMessageParamUnion{openai.SystemMessage(text)}, nil
	case RoleUser:
		return []openai.ChatCompletionMessageParamUnion{openai.UserMessage(text)}, nil
	case RoleAssistant:
		param := openai.AssistantMessage(text)
		for _, block := range message.Blocks {
			if block.Type != BlockToolCall {
				continue
			}
			call := openai.ChatCompletionMessageFunctionToolCallParam{
				ID: block.ToolCallID,
				Function: openai.ChatCompletionMessageFunctionToolCallFunctionParam{
					Name: block.ToolName, Arguments: string(block.Arguments),
				},
			}
			param.OfAssistant.ToolCalls = append(param.OfAssistant.ToolCalls,
				openai.ChatCompletionMessageToolCallUnionParam{OfFunction: &call})
		}
		return []openai.ChatCompletionMessageParamUnion{param}, nil
	case RoleTool:
		result := make([]openai.ChatCompletionMessageParamUnion, 0, len(message.Blocks))
		for _, block := range message.Blocks {
			if block.Type == BlockToolResult {
				result = append(result, openai.ToolMessage(block.Text, block.ToolCallID))
			}
		}
		return result, nil
	default:
		return nil, fmt.Errorf("openai conversion: unsupported role %q", message.Role)
	}
}

func toOpenAITools(definitions []ToolDefinition) []openai.ChatCompletionToolUnionParam {
	tools := make([]openai.ChatCompletionToolUnionParam, 0, len(definitions))
	for _, definition := range definitions {
		tools = append(tools, openai.ChatCompletionFunctionTool(shared.FunctionDefinitionParam{
			Name: definition.Name, Description: openai.String(definition.Description),
			Parameters: shared.FunctionParameters(definition.InputSchema),
		}))
	}
	return tools
}

func openAIToolCalls(items []openai.ChatCompletionMessageToolCallUnion) []ToolCall {
	result := make([]ToolCall, 0, len(items))
	for _, item := range items {
		if item.Type != "function" {
			continue
		}
		args := json.RawMessage(item.Function.Arguments)
		if !json.Valid(args) {
			args = json.RawMessage(`{}`)
		}
		result = append(result, ToolCall{ID: item.ID, Name: item.Function.Name, Arguments: args})
	}
	return result
}

func blocksText(blocks []ContentBlock) string {
	var builder strings.Builder
	for _, block := range blocks {
		if block.Type == BlockText || block.Type == BlockThinking || block.Type == BlockToolResult {
			builder.WriteString(block.Text)
		}
	}
	return builder.String()
}

func assistantResponseMessage(text string, calls []ToolCall) Message {
	blocks := make([]ContentBlock, 0, len(calls)+1)
	if text != "" {
		blocks = append(blocks, ContentBlock{Type: BlockText, Text: text})
	}
	for _, call := range calls {
		blocks = append(blocks, ContentBlock{
			Type: BlockToolCall, ToolCallID: call.ID, ToolName: call.Name, Arguments: call.Arguments,
		})
	}
	return Message{Role: RoleAssistant, Blocks: blocks}
}
