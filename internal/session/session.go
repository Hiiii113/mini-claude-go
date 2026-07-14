// Package session 负责把中立消息历史持久化到用户目录，并迁移旧版 TypeScript 会话。
package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"mini-claude/internal/llm"
)

const currentVersion = 1

// Metadata 是会话列表与恢复所需的轻量信息。
type Metadata struct {
	ID           string    `json:"id"`
	Model        string    `json:"model"`
	CWD          string    `json:"cwd"`
	StartTime    time.Time `json:"start_time"`
	UpdatedTime  time.Time `json:"updated_time"`
	MessageCount int       `json:"message_count"`
}

// Data 是磁盘上的版本化中立会话格式。
type Data struct {
	Version  int           `json:"version"`
	Metadata Metadata      `json:"metadata"`
	Messages []llm.Message `json:"messages"`
	Usage    llm.Usage     `json:"usage,omitempty"`
}

// Store 管理一个会话目录，默认位于 ~/.mini-claude/sessions。
type Store struct {
	dir string
}

// NewStore 创建用户级会话存储。
func NewStore(home string) *Store {
	return &Store{dir: filepath.Join(home, ".mini-claude", "sessions")}
}

// NewStoreAt 创建指定目录的会话存储，主要用于测试和嵌入场景。
func NewStoreAt(dir string) *Store {
	return &Store{dir: dir}
}

// Save 保存会话；文件权限限制为当前用户可读写。
func (s *Store) Save(data Data) error {
	if data.Metadata.ID == "" {
		return errors.New("session id is required")
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("create session directory: %w", err)
	}
	now := time.Now().UTC()
	data.Version = currentVersion
	if data.Metadata.StartTime.IsZero() {
		data.Metadata.StartTime = now
	}
	data.Metadata.UpdatedTime = now
	data.Metadata.MessageCount = len(data.Messages)
	encoded, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("encode session: %w", err)
	}
	path := filepath.Join(s.dir, data.Metadata.ID+".json")
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		return fmt.Errorf("write session: %w", err)
	}
	return nil
}

// Load 读取一个会话；遇到旧格式时会在内存中转换为中立消息。
func (s *Store) Load(id string) (Data, error) {
	content, err := os.ReadFile(filepath.Join(s.dir, id+".json"))
	if err != nil {
		return Data{}, err
	}
	return decode(content)
}

// Latest 返回最近更新的有效会话。
func (s *Store) Latest() (Data, error) {
	items, err := s.List()
	if err != nil {
		return Data{}, err
	}
	if len(items) == 0 {
		return Data{}, os.ErrNotExist
	}
	return s.Load(items[0].ID)
}

// List 按最近更新时间倒序返回可读取会话的元数据。
func (s *Store) List() ([]Metadata, error) {
	entries, err := os.ReadDir(s.dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	result := make([]Metadata, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		content, readErr := os.ReadFile(filepath.Join(s.dir, entry.Name()))
		if readErr != nil {
			continue
		}
		data, decodeErr := decode(content)
		if decodeErr == nil {
			result = append(result, data.Metadata)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		left, right := result[i].UpdatedTime, result[j].UpdatedTime
		if left.IsZero() {
			left = result[i].StartTime
		}
		if right.IsZero() {
			right = result[j].StartTime
		}
		return left.After(right)
	})
	return result, nil
}

func decode(content []byte) (Data, error) {
	var envelope struct {
		Version           int             `json:"version"`
		Metadata          json.RawMessage `json:"metadata"`
		Messages          json.RawMessage `json:"messages"`
		Usage             llm.Usage       `json:"usage"`
		AnthropicMessages json.RawMessage `json:"anthropicMessages"`
		OpenAIMessages    json.RawMessage `json:"openaiMessages"`
	}
	if err := json.Unmarshal(content, &envelope); err != nil {
		return Data{}, fmt.Errorf("decode session: %w", err)
	}
	var metadata Metadata
	if err := decodeMetadata(envelope.Metadata, &metadata); err != nil {
		return Data{}, err
	}
	data := Data{Version: envelope.Version, Metadata: metadata, Usage: envelope.Usage}
	if len(envelope.Messages) > 0 && string(envelope.Messages) != "null" {
		if err := json.Unmarshal(envelope.Messages, &data.Messages); err != nil {
			return Data{}, fmt.Errorf("decode neutral messages: %w", err)
		}
		return data, nil
	}
	var err error
	if len(envelope.AnthropicMessages) > 0 && string(envelope.AnthropicMessages) != "null" {
		data.Messages, err = migrateAnthropic(envelope.AnthropicMessages)
	} else if len(envelope.OpenAIMessages) > 0 && string(envelope.OpenAIMessages) != "null" {
		data.Messages, err = migrateOpenAI(envelope.OpenAIMessages)
	}
	if err != nil {
		return Data{}, err
	}
	data.Version = currentVersion
	data.Metadata.MessageCount = len(data.Messages)
	return data, nil
}

func decodeMetadata(raw json.RawMessage, metadata *Metadata) error {
	if len(raw) == 0 {
		return errors.New("session metadata is missing")
	}
	// 先检查字段风格；直接解码旧 JSON 会静默忽略 startTime，导致“最近会话”排序错误。
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return fmt.Errorf("decode session metadata: %w", err)
	}
	if _, current := fields["start_time"]; current {
		if err := json.Unmarshal(raw, metadata); err != nil {
			return fmt.Errorf("decode session metadata: %w", err)
		}
		if metadata.ID == "" {
			return errors.New("session metadata id is missing")
		}
		return nil
	}
	var legacy struct {
		ID           string `json:"id"`
		Model        string `json:"model"`
		CWD          string `json:"cwd"`
		StartTime    string `json:"startTime"`
		MessageCount int    `json:"messageCount"`
	}
	if err := json.Unmarshal(raw, &legacy); err != nil {
		return fmt.Errorf("decode session metadata: %w", err)
	}
	if legacy.ID == "" {
		return errors.New("session metadata id is missing")
	}
	start, _ := time.Parse(time.RFC3339, legacy.StartTime)
	*metadata = Metadata{ID: legacy.ID, Model: legacy.Model, CWD: legacy.CWD, StartTime: start, UpdatedTime: start, MessageCount: legacy.MessageCount}
	return nil
}

func migrateOpenAI(raw json.RawMessage) ([]llm.Message, error) {
	var messages []map[string]any
	if err := json.Unmarshal(raw, &messages); err != nil {
		return nil, fmt.Errorf("migrate OpenAI session: %w", err)
	}
	result := make([]llm.Message, 0, len(messages))
	for _, item := range messages {
		role, _ := item["role"].(string)
		text, _ := item["content"].(string)
		message := llm.Message{Role: llm.Role(role)}
		if text != "" {
			message.Blocks = append(message.Blocks, llm.ContentBlock{Type: llm.BlockText, Text: text})
		}
		if role == "tool" {
			message.Role = llm.RoleTool
			message.Blocks = []llm.ContentBlock{{Type: llm.BlockToolResult, Text: text, ToolCallID: stringValue(item["tool_call_id"])}}
		}
		if calls, ok := item["tool_calls"].([]any); ok {
			for _, value := range calls {
				call, _ := value.(map[string]any)
				function, _ := call["function"].(map[string]any)
				arguments := json.RawMessage(stringValue(function["arguments"]))
				if !json.Valid(arguments) {
					arguments = json.RawMessage(`{}`)
				}
				message.Blocks = append(message.Blocks, llm.ContentBlock{Type: llm.BlockToolCall, ToolCallID: stringValue(call["id"]), ToolName: stringValue(function["name"]), Arguments: arguments})
			}
		}
		result = append(result, message)
	}
	return result, nil
}

func migrateAnthropic(raw json.RawMessage) ([]llm.Message, error) {
	var messages []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(raw, &messages); err != nil {
		return nil, fmt.Errorf("migrate Anthropic session: %w", err)
	}
	result := make([]llm.Message, 0, len(messages))
	for _, item := range messages {
		message := llm.Message{Role: llm.Role(item.Role)}
		var text string
		if json.Unmarshal(item.Content, &text) == nil {
			message.Blocks = []llm.ContentBlock{{Type: llm.BlockText, Text: text}}
			result = append(result, message)
			continue
		}
		var blocks []map[string]any
		if err := json.Unmarshal(item.Content, &blocks); err != nil {
			return nil, fmt.Errorf("migrate Anthropic content: %w", err)
		}
		for _, block := range blocks {
			switch stringValue(block["type"]) {
			case "text":
				message.Blocks = append(message.Blocks, llm.ContentBlock{Type: llm.BlockText, Text: stringValue(block["text"])})
			case "thinking":
				message.Blocks = append(message.Blocks, llm.ContentBlock{Type: llm.BlockThinking, Text: stringValue(block["thinking"]), Signature: stringValue(block["signature"])})
			case "tool_use":
				arguments, _ := json.Marshal(block["input"])
				message.Blocks = append(message.Blocks, llm.ContentBlock{Type: llm.BlockToolCall, ToolCallID: stringValue(block["id"]), ToolName: stringValue(block["name"]), Arguments: arguments})
			case "tool_result":
				message.Role = llm.RoleTool
				message.Blocks = append(message.Blocks, llm.ContentBlock{Type: llm.BlockToolResult, ToolCallID: stringValue(block["tool_use_id"]), Text: stringValue(block["content"]), IsError: boolValue(block["is_error"])})
			}
		}
		result = append(result, message)
	}
	return result, nil
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func boolValue(value any) bool {
	result, _ := value.(bool)
	return result
}
