// Package skills 发现并解析用户级和项目级 SKILL.md。
package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"mini-claude/internal/frontmatter"
)

// ContextMode 决定技能在父 Agent 内联执行还是使用隔离子 Agent。
type ContextMode string

const (
	ContextInline ContextMode = "inline"
	ContextFork   ContextMode = "fork"
)

// Definition 是一个已解析的技能定义。
type Definition struct {
	Name           string
	Description    string
	WhenToUse      string
	AllowedTools   []string
	UserInvocable  bool
	Context        ContextMode
	PromptTemplate string
	Source         string
	Dir            string
}

// Registry 缓存技能发现结果；Refresh 可显式刷新磁盘变化。
type Registry struct {
	home, cwd string
	mu        sync.RWMutex
	cache     []Definition
	loaded    bool
}

// NewRegistry 创建技能注册表。
func NewRegistry(home, cwd string) *Registry { return &Registry{home: home, cwd: cwd} }

// Discover 返回项目优先合并后的技能列表。
func (r *Registry) Discover() []Definition {
	r.mu.RLock()
	if r.loaded {
		result := append([]Definition(nil), r.cache...)
		r.mu.RUnlock()
		return result
	}
	r.mu.RUnlock()
	merged := map[string]Definition{}
	loadDir(filepath.Join(r.home, ".claude", "skills"), "user", merged)
	loadDir(filepath.Join(r.cwd, ".claude", "skills"), "project", merged)
	result := make([]Definition, 0, len(merged))
	for _, skill := range merged {
		result = append(result, skill)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	r.mu.Lock()
	r.cache, r.loaded = result, true
	r.mu.Unlock()
	return append([]Definition(nil), result...)
}

// Refresh 清空发现缓存。
func (r *Registry) Refresh() { r.mu.Lock(); r.cache, r.loaded = nil, false; r.mu.Unlock() }

// Get 按名称查找技能。
func (r *Registry) Get(name string) (Definition, bool) {
	for _, skill := range r.Discover() {
		if skill.Name == name {
			return skill, true
		}
	}
	return Definition{}, false
}

// Resolve 替换技能参数和技能目录占位符。
func Resolve(skill Definition, arguments string) string {
	prompt := strings.ReplaceAll(skill.PromptTemplate, "${ARGUMENTS}", arguments)
	prompt = strings.ReplaceAll(prompt, "$ARGUMENTS", arguments)
	return strings.ReplaceAll(prompt, "${CLAUDE_SKILL_DIR}", skill.Dir)
}

// Descriptions 生成系统提示词中的精简技能清单。
func (r *Registry) Descriptions() string {
	items := r.Discover()
	if len(items) == 0 {
		return ""
	}
	lines := []string{"Available skills:"}
	for _, skill := range items {
		name := skill.Name
		if skill.UserInvocable {
			name = "/" + name
		}
		lines = append(lines, fmt.Sprintf("- %s: %s", name, skill.Description))
		if skill.WhenToUse != "" {
			lines = append(lines, "  When to use: "+skill.WhenToUse)
		}
	}
	return strings.Join(lines, "\n")
}

func loadDir(base, source string, result map[string]Definition) {
	entries, err := os.ReadDir(base)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(base, entry.Name())
		content, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
		if err != nil {
			continue
		}
		document, err := frontmatter.Parse(string(content))
		if err != nil {
			continue
		}
		name := frontmatter.String(document.Meta, "name")
		if name == "" {
			name = entry.Name()
		}
		mode := ContextInline
		if frontmatter.String(document.Meta, "context") == string(ContextFork) {
			mode = ContextFork
		}
		when := frontmatter.String(document.Meta, "when-to-use")
		if when == "" {
			when = frontmatter.String(document.Meta, "when_to_use")
		}
		result[name] = Definition{
			Name: name, Description: frontmatter.String(document.Meta, "description"), WhenToUse: when,
			AllowedTools:  frontmatter.Strings(document.Meta, "allowed-tools"),
			UserInvocable: frontmatter.Bool(document.Meta, "user-invocable", true), Context: mode,
			PromptTemplate: document.Body, Source: source, Dir: dir,
		}
	}
}
