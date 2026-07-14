package tools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bmatcuk/doublestar/v4"

	"mini-claude/internal/llm"
)

const maxResultChars = 50_000

// Handler 是一个工具的执行函数。
type Handler func(ctx context.Context, arguments json.RawMessage) (string, error)

type registeredTool struct {
	definition      llm.ToolDefinition
	handler         Handler
	concurrencySafe bool
}

// Registry 管理工具定义、处理函数、延迟激活状态和 read-before-edit 状态。
type Registry struct {
	mu        sync.RWMutex
	tools     map[string]registeredTool
	activated map[string]bool
	readState map[string]time.Time
	root      string
	http      *http.Client
}

// NewRegistry 创建空工具注册表。
func NewRegistry(root string) *Registry {
	if root == "" {
		root, _ = os.Getwd()
	}
	return &Registry{
		tools: map[string]registeredTool{}, activated: map[string]bool{}, readState: map[string]time.Time{},
		root: root,
		http: &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(req *http.Request, _ []*http.Request) error {
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return errors.New("redirected to unsupported URL scheme")
			}
			return nil
		}},
	}
}

// DefaultRegistry 创建与原项目兼容的12个静态工具。
func DefaultRegistry(root string) *Registry {
	r := NewRegistry(root)
	r.Register(def("read_file", "Read a file and return numbered lines.", objectSchema(map[string]any{
		"file_path": stringProperty("Path of the file to read"),
	}, "file_path")), r.readFile, true)
	r.Register(def("write_file", "Create or overwrite a file.", objectSchema(map[string]any{
		"file_path": stringProperty("Path of the file to write"), "content": stringProperty("Complete file content"),
	}, "file_path", "content")), r.writeFile, false)
	r.Register(def("edit_file", "Replace one exact string in a file.", objectSchema(map[string]any{
		"file_path": stringProperty("Path of the file to edit"), "old_string": stringProperty("Exact old text"),
		"new_string": stringProperty("Replacement text"),
	}, "file_path", "old_string", "new_string")), r.editFile, false)
	r.Register(def("list_files", "List files matching a glob pattern.", objectSchema(map[string]any{
		"pattern": stringProperty("Glob pattern"), "path": stringProperty("Optional base directory"),
	}, "pattern")), r.listFiles, true)
	r.Register(def("grep_search", "Search file content with a regular expression.", objectSchema(map[string]any{
		"pattern": stringProperty("Regular expression"), "path": stringProperty("Directory or file"),
		"include": stringProperty("Optional include glob"),
	}, "pattern")), r.grepSearch, true)
	r.Register(def("run_shell", "Execute a shell command.", objectSchema(map[string]any{
		"command": stringProperty("Shell command"), "timeout": map[string]any{"type": "number"},
	}, "command")), r.runShell, false)
	r.Register(def("web_fetch", "Fetch an HTTP or HTTPS URL as text.", objectSchema(map[string]any{
		"url": stringProperty("URL to fetch"), "max_length": map[string]any{"type": "number"},
	}, "url")), r.webFetch, true)
	r.Register(def("skill", "Invoke a registered skill.", objectSchema(map[string]any{
		"skill_name": stringProperty("Skill name"), "args": stringProperty("Optional arguments"),
	}, "skill_name")), unsupported("skill is handled by the agent"), false)
	r.Register(deferredDef("enter_plan_mode", "Enter read-only plan mode."), unsupported("plan mode is handled by the agent"), false)
	r.Register(deferredDef("exit_plan_mode", "Exit plan mode and request approval."), unsupported("plan mode is handled by the agent"), false)
	r.Register(def("agent", "Launch an isolated sub-agent.", objectSchema(map[string]any{
		"description": stringProperty("Short task description"), "prompt": stringProperty("Detailed task"),
		"type": stringProperty("Agent type: explore, plan, general, or a custom .claude/agents name"),
	}, "description", "prompt")), unsupported("sub-agent is handled by the agent"), false)
	r.Register(def("tool_search", "Activate deferred tools matching a query.", objectSchema(map[string]any{
		"query": stringProperty("Tool name or keyword"),
	}, "query")), r.toolSearch, true)
	return r
}

// Register 注册或覆盖一个工具，主要用于内置扩展和测试。
func (r *Registry) Register(definition llm.ToolDefinition, handler Handler, concurrencySafe bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[definition.Name] = registeredTool{definition: definition, handler: handler, concurrencySafe: concurrencySafe}
}

// Activate 激活一个或多个延迟工具。
func (r *Registry) Activate(names ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, name := range names {
		if item, ok := r.tools[name]; ok && item.definition.Deferred {
			r.activated[name] = true
		}
	}
}

// Subset 创建共享处理函数但拥有独立定义集合的子注册表，用于受限子 Agent。
func (r *Registry) Subset(allowed []string, excluded ...string) *Registry {
	allow := map[string]bool{}
	for _, name := range allowed {
		allow[name] = true
	}
	deny := map[string]bool{}
	for _, name := range excluded {
		deny[name] = true
	}
	result := NewRegistry(r.root)
	r.mu.RLock()
	defer r.mu.RUnlock()
	for name, item := range r.tools {
		if deny[name] || (len(allow) > 0 && !allow[name]) {
			continue
		}
		result.tools[name] = item
		result.activated[name] = r.activated[name]
	}
	return result
}

// Definitions 返回当前应发送给模型的工具定义。
func (r *Registry) Definitions() []llm.ToolDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]llm.ToolDefinition, 0, len(r.tools))
	for _, item := range r.tools {
		if !item.definition.Deferred || r.activated[item.definition.Name] {
			definition := item.definition
			definition.Deferred = false
			result = append(result, definition)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

// IsConcurrencySafe 判断工具是否可以和其他只读工具并行运行。
func (r *Registry) IsConcurrencySafe(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.tools[name].concurrencySafe
}

// Execute 校验工具名称并调用其处理函数。
func (r *Registry) Execute(ctx context.Context, call llm.ToolCall) (string, error) {
	r.mu.RLock()
	item, ok := r.tools[call.Name]
	r.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("unknown tool: %s", call.Name)
	}
	result, err := item.handler(ctx, call.Arguments)
	return TruncateResult(result), err
}

// TruncateResult 限制进入模型上下文的异常超长结果。
func TruncateResult(result string) string {
	if len(result) <= maxResultChars {
		return result
	}
	keep := (maxResultChars - 80) / 2
	return result[:keep] + fmt.Sprintf("\n\n[... truncated %d chars ...]\n\n", len(result)-keep*2) + result[len(result)-keep:]
}

func (r *Registry) readFile(_ context.Context, raw json.RawMessage) (string, error) {
	var input struct {
		FilePath string `json:"file_path"`
	}
	if err := decode(raw, &input); err != nil {
		return "", err
	}
	path := r.resolve(input.FilePath)
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", errors.New("path is a directory")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	r.mu.Lock()
	r.readState[path] = info.ModTime()
	r.mu.Unlock()
	lines := strings.Split(string(data), "\n")
	var output strings.Builder
	for index, line := range lines {
		fmt.Fprintf(&output, "%4d | %s", index+1, line)
		if index < len(lines)-1 {
			output.WriteByte('\n')
		}
	}
	return output.String(), nil
}

func (r *Registry) writeFile(_ context.Context, raw json.RawMessage) (string, error) {
	var input struct {
		FilePath string `json:"file_path"`
		Content  string `json:"content"`
	}
	if err := decode(raw, &input); err != nil {
		return "", err
	}
	path := r.resolve(input.FilePath)
	if _, err := os.Stat(path); err == nil {
		if err := r.verifyRead(path); err != nil {
			return "", err
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(input.Content), 0o644); err != nil {
		return "", err
	}
	r.refreshReadState(path)
	return fmt.Sprintf("Successfully wrote to %s (%d lines)", path, lineCount(input.Content)), nil
}

func (r *Registry) editFile(_ context.Context, raw json.RawMessage) (string, error) {
	var input struct {
		FilePath string `json:"file_path"`
		Old      string `json:"old_string"`
		New      string `json:"new_string"`
	}
	if err := decode(raw, &input); err != nil {
		return "", err
	}
	path := r.resolve(input.FilePath)
	if err := r.verifyRead(path); err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	content := string(data)
	old := input.Old
	note := ""
	if strings.Count(content, old) == 0 {
		normalized := normalizeQuotes(old)
		if normalized != old && strings.Count(content, normalized) > 0 {
			old = normalized
			note = " (matched via quote normalization)"
		}
	}
	count := strings.Count(content, old)
	if count == 0 {
		return "", errors.New("old_string was not found")
	}
	if count != 1 {
		return "", fmt.Errorf("old_string matched %d times; provide a unique match", count)
	}
	updated := strings.Replace(content, old, input.New, 1)
	if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
		return "", err
	}
	r.refreshReadState(path)
	return fmt.Sprintf("Successfully edited %s%s", path, note), nil
}

func (r *Registry) listFiles(_ context.Context, raw json.RawMessage) (string, error) {
	var input struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
	}
	if err := decode(raw, &input); err != nil {
		return "", err
	}
	base := r.root
	if input.Path != "" {
		base = r.resolve(input.Path)
	}
	matches, err := doublestar.Glob(os.DirFS(base), filepath.ToSlash(input.Pattern), doublestar.WithFilesOnly())
	if err != nil {
		return "", err
	}
	sort.Strings(matches)
	return strings.Join(matches, "\n"), nil
}

func (r *Registry) grepSearch(ctx context.Context, raw json.RawMessage) (string, error) {
	var input struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
		Include string `json:"include"`
	}
	if err := decode(raw, &input); err != nil {
		return "", err
	}
	pattern, err := regexp.Compile(input.Pattern)
	if err != nil {
		return "", err
	}
	target := r.root
	if input.Path != "" {
		target = r.resolve(input.Path)
	}
	var output strings.Builder
	err = filepath.WalkDir(target, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == ".git" || entry.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if input.Include != "" {
			matched, _ := doublestar.Match(filepath.ToSlash(input.Include), filepath.ToSlash(entry.Name()))
			if !matched {
				return nil
			}
		}
		info, err := entry.Info()
		if err != nil || info.Size() > 2*1024*1024 {
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			return nil
		}
		scanner := bufio.NewScanner(file)
		line := 0
		for scanner.Scan() {
			line++
			if pattern.Match(scanner.Bytes()) {
				relative, _ := filepath.Rel(r.root, path)
				fmt.Fprintf(&output, "%s:%d:%s\n", filepath.ToSlash(relative), line, scanner.Text())
			}
		}
		_ = file.Close()
		return nil
	})
	return strings.TrimSuffix(output.String(), "\n"), err
}

func (r *Registry) runShell(ctx context.Context, raw json.RawMessage) (string, error) {
	var input struct {
		Command string `json:"command"`
		Timeout int    `json:"timeout"`
	}
	if err := decode(raw, &input); err != nil {
		return "", err
	}
	timeout := 30 * time.Second
	if input.Timeout > 0 {
		timeout = time.Duration(input.Timeout) * time.Millisecond
		if timeout > 10*time.Minute {
			timeout = 10 * time.Minute
		}
	}
	commandCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var command *exec.Cmd
	if runtime.GOOS == "windows" {
		command = exec.CommandContext(commandCtx, "powershell", "-NoProfile", "-NonInteractive", "-Command", input.Command)
	} else {
		command = exec.CommandContext(commandCtx, "/bin/sh", "-lc", input.Command)
	}
	command.Dir = r.root
	output, err := command.CombinedOutput()
	if errors.Is(commandCtx.Err(), context.DeadlineExceeded) {
		return string(output), fmt.Errorf("command timed out after %s", timeout)
	}
	if err != nil {
		return string(output), fmt.Errorf("command failed: %w", err)
	}
	return string(output), nil
}

func (r *Registry) webFetch(ctx context.Context, raw json.RawMessage) (string, error) {
	var input struct {
		URL       string `json:"url"`
		MaxLength int64  `json:"max_length"`
	}
	if err := decode(raw, &input); err != nil {
		return "", err
	}
	parsed, err := url.Parse(input.URL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return "", errors.New("web_fetch only supports absolute http/https URLs")
	}
	limit := input.MaxLength
	if limit <= 0 || limit > 1_000_000 {
		limit = 50_000
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("User-Agent", "mini-claude-go/1.0")
	response, err := r.http.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("HTTP %s", response.Status)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return "", err
	}
	if int64(len(data)) > limit {
		data = data[:limit]
	}
	content := string(data)
	if strings.Contains(strings.ToLower(response.Header.Get("Content-Type")), "text/html") {
		content = htmlToText(content)
	}
	return content, nil
}

func (r *Registry) toolSearch(_ context.Context, raw json.RawMessage) (string, error) {
	var input struct {
		Query string `json:"query"`
	}
	if err := decode(raw, &input); err != nil {
		return "", err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	query := strings.ToLower(input.Query)
	var found []llm.ToolDefinition
	for name, item := range r.tools {
		if item.definition.Deferred && (strings.Contains(strings.ToLower(name), query) || strings.Contains(strings.ToLower(item.definition.Description), query)) {
			r.activated[name] = true
			found = append(found, item.definition)
		}
	}
	data, err := json.MarshalIndent(found, "", "  ")
	return string(data), err
}

func (r *Registry) resolve(path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Clean(filepath.Join(r.root, path))
}

func (r *Registry) verifyRead(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	r.mu.RLock()
	readTime, ok := r.readState[path]
	r.mu.RUnlock()
	if !ok {
		return errors.New("you must read this file before editing it")
	}
	if !info.ModTime().Equal(readTime) {
		return errors.New("file changed since it was read; read it again before editing")
	}
	return nil
}

func (r *Registry) refreshReadState(path string) {
	info, err := os.Stat(path)
	if err == nil {
		r.mu.Lock()
		r.readState[path] = info.ModTime()
		r.mu.Unlock()
	}
}

func decode(raw json.RawMessage, target any) error {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("invalid tool input: %w", err)
	}
	return nil
}

func def(name, description string, schema map[string]any) llm.ToolDefinition {
	return llm.ToolDefinition{Name: name, Description: description, InputSchema: schema}
}

func deferredDef(name, description string) llm.ToolDefinition {
	definition := def(name, description, objectSchema(map[string]any{}))
	definition.Deferred = true
	return definition
}

func objectSchema(properties map[string]any, required ...string) map[string]any {
	return map[string]any{"type": "object", "properties": properties, "required": required}
}

func stringProperty(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

func unsupported(message string) Handler {
	return func(context.Context, json.RawMessage) (string, error) { return "", errors.New(message) }
}

func normalizeQuotes(value string) string {
	replacer := strings.NewReplacer("“", `"`, "”", `"`, "‘", "'", "’", "'")
	return replacer.Replace(value)
}

func lineCount(value string) int {
	if value == "" {
		return 0
	}
	return strings.Count(value, "\n") + 1
}

var htmlTag = regexp.MustCompile(`(?s)<[^>]*>`)
var htmlSpace = regexp.MustCompile(`\s+`)

func htmlToText(value string) string {
	// Go 的正则表达式不支持反向引用，因此分别匹配 script 和 style，避免初始化时 panic。
	value = regexp.MustCompile(`(?is)<script[^>]*>.*?</script>|<style[^>]*>.*?</style>`).ReplaceAllString(value, " ")
	value = htmlTag.ReplaceAllString(value, " ")
	return strings.TrimSpace(htmlSpace.ReplaceAllString(html.UnescapeString(value), " "))
}
