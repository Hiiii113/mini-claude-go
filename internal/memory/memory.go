// Package memory 实现按项目隔离的四类文件记忆、索引和语义召回。
package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"mini-claude/internal/frontmatter"
	"mini-claude/internal/llm"
)

const (
	maxIndexLines         = 200
	maxIndexBytes         = 25_000
	maxMemoryFiles        = 200
	maxMemoryBytes        = 4_096
	MaxSessionMemoryBytes = 60 * 1024
)

// Type 是支持的四种记忆类别。
type Type string

const (
	TypeUser      Type = "user"
	TypeFeedback  Type = "feedback"
	TypeProject   Type = "project"
	TypeReference Type = "reference"
)

// Entry 是一个完整记忆文件。
type Entry struct {
	Name, Description, Filename, Content string
	Type                                 Type
	ModTime                              time.Time
}

// Relevant 是一次召回后准备注入上下文的记忆。
type Relevant struct {
	Path, Header, Content string
	ModTime               time.Time
}

// PrefetchResult 是异步召回结果。
type PrefetchResult struct {
	Memories []Relevant
	Err      error
}

// Prefetch 是可等待的异步语义召回句柄。
type Prefetch struct{ Done <-chan PrefetchResult }

// Store 管理当前项目的记忆目录。
type Store struct {
	dir string
}

// NewStore 使用项目绝对路径哈希创建隔离目录。
func NewStore(home, cwd string) *Store {
	abs, err := filepath.Abs(cwd)
	if err != nil {
		abs = filepath.Clean(cwd)
	}
	hash := sha256.Sum256([]byte(strings.ToLower(abs)))
	projectID := hex.EncodeToString(hash[:])[:16]
	return &Store{dir: filepath.Join(home, ".mini-claude", "projects", projectID, "memory")}
}

// NewStoreAt 创建指定目录的记忆存储，主要用于测试。
func NewStoreAt(dir string) *Store { return &Store{dir: dir} }

// Dir 返回记忆目录。
func (s *Store) Dir() string { return s.dir }

// Save 保存记忆并重建 MEMORY.md 索引。
func (s *Store) Save(entry Entry) (string, error) {
	if entry.Name == "" || !validType(entry.Type) {
		return "", errors.New("memory name and valid type are required")
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return "", err
	}
	filename := string(entry.Type) + "_" + slug(entry.Name) + ".md"
	content, err := frontmatter.Format(map[string]any{"name": entry.Name, "description": entry.Description, "type": string(entry.Type)}, entry.Content)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(s.dir, filename), []byte(content), 0o600); err != nil {
		return "", err
	}
	return filename, s.updateIndex()
}

// Delete 删除指定文件名的记忆并重建索引。
func (s *Store) Delete(filename string) (bool, error) {
	if filename != filepath.Base(filename) || filename == "MEMORY.md" {
		return false, errors.New("invalid memory filename")
	}
	err := os.Remove(filepath.Join(s.dir, filename))
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, s.updateIndex()
}

// List 按修改时间倒序返回最多 200 个有效记忆。
func (s *Store) List() ([]Entry, error) {
	entries, err := os.ReadDir(s.dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	result := make([]Entry, 0, len(entries))
	for _, file := range entries {
		if file.IsDir() || filepath.Ext(file.Name()) != ".md" || file.Name() == "MEMORY.md" {
			continue
		}
		path := filepath.Join(s.dir, file.Name())
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			continue
		}
		document, parseErr := frontmatter.Parse(string(content))
		if parseErr != nil {
			continue
		}
		entryType := Type(frontmatter.String(document.Meta, "type"))
		name := frontmatter.String(document.Meta, "name")
		if name == "" || !validType(entryType) {
			continue
		}
		info, _ := file.Info()
		entry := Entry{Name: name, Description: frontmatter.String(document.Meta, "description"), Type: entryType, Filename: file.Name(), Content: document.Body}
		if info != nil {
			entry.ModTime = info.ModTime()
		}
		result = append(result, entry)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ModTime.After(result[j].ModTime) })
	if len(result) > maxMemoryFiles {
		result = result[:maxMemoryFiles]
	}
	return result, nil
}

// LoadIndex 读取受行数和字节数限制的 MEMORY.md。
func (s *Store) LoadIndex() string {
	content, err := os.ReadFile(filepath.Join(s.dir, "MEMORY.md"))
	if err != nil {
		return ""
	}
	lines := strings.Split(string(content), "\n")
	if len(lines) > maxIndexLines {
		lines = append(lines[:maxIndexLines], "[... truncated, too many memory entries ...]")
	}
	result := strings.Join(lines, "\n")
	if len(result) > maxIndexBytes {
		result = result[:maxIndexBytes] + "\n[... truncated, index too large ...]"
	}
	return result
}

// PromptSection 生成系统提示词中的记忆说明与当前索引。
func (s *Store) PromptSection() string {
	index := s.LoadIndex()
	if index == "" {
		index = "(No memories saved yet.)"
	}
	return fmt.Sprintf("Persistent memory directory: %s\nTypes: user, feedback, project, reference.\nCurrent index:\n%s", s.dir, index)
}

// RefreshIndex 重扫记忆目录并重建 MEMORY.md，供通用文件工具写入后调用。
func (s *Store) RefreshIndex() error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	return s.updateIndex()
}

// Contains 判断路径是否位于本存储目录中。
func (s *Store) Contains(path string) bool {
	absPath, errPath := filepath.Abs(path)
	absDir, errDir := filepath.Abs(s.dir)
	if errPath != nil || errDir != nil {
		return false
	}
	relative, err := filepath.Rel(absDir, absPath)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

// StartPrefetch 异步选择最多五条相关记忆；短查询和超预算会直接返回 nil。
func (s *Store) StartPrefetch(ctx context.Context, query string, client llm.Client, model string, surfaced map[string]bool, sessionBytes int) *Prefetch {
	if !substantial(query) || sessionBytes >= MaxSessionMemoryBytes {
		return nil
	}
	items, err := s.List()
	if err != nil || len(items) == 0 {
		return nil
	}
	done := make(chan PrefetchResult, 1)
	go func() {
		memories, selectErr := s.SelectRelevant(ctx, query, client, model, surfaced)
		done <- PrefetchResult{Memories: memories, Err: selectErr}
		close(done)
	}()
	return &Prefetch{Done: done}
}

// SelectRelevant 使用配置的模型从索引描述中语义选择相关记忆。
func (s *Store) SelectRelevant(ctx context.Context, query string, client llm.Client, model string, surfaced map[string]bool) ([]Relevant, error) {
	items, err := s.List()
	if err != nil {
		return nil, err
	}
	byName := map[string]Entry{}
	var manifest []string
	for _, item := range items {
		path := filepath.Join(s.dir, item.Filename)
		if surfaced[path] {
			continue
		}
		byName[item.Filename] = item
		manifest = append(manifest, fmt.Sprintf("- [%s] %s: %s", item.Type, item.Filename, item.Description))
	}
	if len(manifest) == 0 {
		return nil, nil
	}
	response, err := client.Complete(ctx, llm.Request{
		Model: model, MaxTokens: 256,
		System:   "Select only clearly relevant memory files. Return JSON: {\"selected_memories\":[\"filename.md\"]}, at most 5.",
		Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "Query: "+query+"\n\nAvailable memories:\n"+strings.Join(manifest, "\n"))},
	})
	if err != nil {
		return nil, err
	}
	start, end := strings.Index(response.Text, "{"), strings.LastIndex(response.Text, "}")
	if start < 0 || end < start {
		return nil, nil
	}
	var selected struct {
		Names []string `json:"selected_memories"`
	}
	if err := json.Unmarshal([]byte(response.Text[start:end+1]), &selected); err != nil {
		return nil, fmt.Errorf("decode memory selection: %w", err)
	}
	result := make([]Relevant, 0, 5)
	for _, name := range selected.Names {
		item, ok := byName[name]
		if !ok {
			continue
		}
		path := filepath.Join(s.dir, name)
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			continue
		}
		if len(content) > maxMemoryBytes {
			content = append(content[:maxMemoryBytes], []byte("\n[... truncated, memory file too large ...]")...)
		}
		header := fmt.Sprintf("Memory (saved %s): %s", age(item.ModTime), path)
		if warning := freshness(item.ModTime); warning != "" {
			header = warning + "\n" + header
		}
		result = append(result, Relevant{Path: path, Header: header, Content: string(content), ModTime: item.ModTime})
		if len(result) == 5 {
			break
		}
	}
	return result, nil
}

// FormatInjection 把召回结果包装成系统提醒块。
func FormatInjection(memories []Relevant) string {
	parts := make([]string, 0, len(memories))
	for _, item := range memories {
		parts = append(parts, "<system-reminder>\n"+item.Header+"\n\n"+item.Content+"\n</system-reminder>")
	}
	return strings.Join(parts, "\n\n")
}

func (s *Store) updateIndex() error {
	items, err := s.List()
	if err != nil {
		return err
	}
	lines := []string{"# Memory Index", ""}
	for _, item := range items {
		lines = append(lines, fmt.Sprintf("- **[%s](%s)** (%s) — %s", item.Name, item.Filename, item.Type, item.Description))
	}
	return os.WriteFile(filepath.Join(s.dir, "MEMORY.md"), []byte(strings.Join(lines, "\n")), 0o600)
}

var slugPattern = regexp.MustCompile(`[^a-z0-9]+`)

func slug(value string) string {
	result := strings.Trim(slugPattern.ReplaceAllString(strings.ToLower(value), "_"), "_")
	if result == "" {
		hash := sha256.Sum256([]byte(value))
		result = hex.EncodeToString(hash[:4])
	}
	if len(result) > 40 {
		result = result[:40]
	}
	return result
}

func validType(value Type) bool {
	return value == TypeUser || value == TypeFeedback || value == TypeProject || value == TypeReference
}

func substantial(query string) bool {
	trimmed := strings.TrimSpace(query)
	if strings.ContainsAny(trimmed, " \t\n") {
		return true
	}
	count := 0
	for _, char := range trimmed {
		if char >= 0x3040 {
			count++
		}
	}
	return count >= 2
}

func age(value time.Time) string {
	days := int(time.Since(value).Hours() / 24)
	if days <= 0 {
		return "today"
	}
	if days == 1 {
		return "yesterday"
	}
	return fmt.Sprintf("%d days ago", days)
}

func freshness(value time.Time) string {
	days := int(time.Since(value).Hours() / 24)
	if days <= 1 {
		return ""
	}
	return fmt.Sprintf("This memory is %d days old; verify time-sensitive claims against current code.", days)
}
