// Package frontmatter 解析和生成 Markdown 文件顶部的 YAML 元数据。
package frontmatter

import (
	"fmt"
	"strings"

	"github.com/goccy/go-yaml"
)

// Document 是 frontmatter 元数据和正文。
type Document struct {
	Meta map[string]any
	Body string
}

// Parse 解析以 --- 包围的 YAML；没有 frontmatter 时把全部内容作为正文。
func Parse(content string) (Document, error) {
	normalized := strings.ReplaceAll(content, "\r\n", "\n")
	if !strings.HasPrefix(normalized, "---\n") {
		return Document{Meta: map[string]any{}, Body: content}, nil
	}
	rest := normalized[4:]
	end := strings.Index(rest, "\n---\n")
	if end < 0 {
		if strings.HasSuffix(rest, "\n---") {
			end = len(rest) - 4
		} else {
			return Document{}, fmt.Errorf("frontmatter closing delimiter is missing")
		}
	}
	meta := map[string]any{}
	if err := yaml.Unmarshal([]byte(rest[:end]), &meta); err != nil {
		return Document{}, fmt.Errorf("parse frontmatter: %w", err)
	}
	bodyStart := end + 4
	if bodyStart < len(rest) && rest[bodyStart] == '\n' {
		bodyStart++
	}
	return Document{Meta: meta, Body: rest[bodyStart:]}, nil
}

// Format 将元数据和正文编码为 Markdown frontmatter。
func Format(meta map[string]any, body string) (string, error) {
	encoded, err := yaml.Marshal(meta)
	if err != nil {
		return "", fmt.Errorf("format frontmatter: %w", err)
	}
	return "---\n" + strings.TrimSpace(string(encoded)) + "\n---\n" + body, nil
}

// String 读取字符串字段。
func String(meta map[string]any, key string) string {
	value, _ := meta[key].(string)
	return value
}

// Bool 读取布尔字段，并兼容 YAML 中的字符串布尔值。
func Bool(meta map[string]any, key string, fallback bool) bool {
	value, ok := meta[key]
	if !ok {
		return fallback
	}
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return strings.ToLower(typed) != "false"
	default:
		return fallback
	}
}

// Strings 读取 YAML 数组或逗号分隔字符串。
func Strings(meta map[string]any, key string) []string {
	value, ok := meta[key]
	if !ok {
		return nil
	}
	var result []string
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
				result = append(result, strings.TrimSpace(text))
			}
		}
	case []string:
		result = append(result, typed...)
	case string:
		for _, item := range strings.Split(strings.Trim(typed, "[]"), ",") {
			if item = strings.TrimSpace(item); item != "" {
				result = append(result, item)
			}
		}
	}
	return result
}
