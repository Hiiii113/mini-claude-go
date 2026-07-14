// Package subagent 定义内置子 Agent 和 .claude/agents/*.md 自定义 Agent。
package subagent

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"mini-claude/internal/frontmatter"
)

var readOnlyTools = []string{"read_file", "list_files", "grep_search"}

const explorePrompt = `You are a read-only codebase exploration specialist. Search broadly, read relevant files, and return concise findings with paths. Never modify files or system state.`
const planPrompt = `You are a read-only planning specialist. Analyze the codebase and return a structured implementation plan with current state, steps, critical files, verification, and risks. Never modify files.`
const generalPrompt = `You are an isolated coding sub-agent. Complete the delegated task using available tools, avoid unrelated changes, and return a concise report of work and findings.`

// Config 是创建隔离子 Agent 所需的提示词和工具白名单。
type Config struct {
	Name, Description, SystemPrompt string
	AllowedTools                    []string
}

// Registry 发现用户级和项目级自定义 Agent，项目定义优先。
type Registry struct {
	home, cwd string
	mu        sync.RWMutex
	cache     map[string]Config
	loaded    bool
}

// NewRegistry 创建子 Agent 注册表。
func NewRegistry(home, cwd string) *Registry { return &Registry{home: home, cwd: cwd} }

// Resolve 返回内置或自定义 Agent；未知类型回退到 general。
func (r *Registry) Resolve(name string) Config {
	if custom, ok := r.custom()[name]; ok {
		return custom
	}
	switch name {
	case "explore":
		return Config{Name: name, Description: "Fast read-only codebase exploration", SystemPrompt: explorePrompt, AllowedTools: append([]string(nil), readOnlyTools...)}
	case "plan":
		return Config{Name: name, Description: "Read-only structured implementation planning", SystemPrompt: planPrompt, AllowedTools: append([]string(nil), readOnlyTools...)}
	default:
		return Config{Name: "general", Description: "General independent task execution", SystemPrompt: generalPrompt}
	}
}

// List 返回三种内置类型和自定义类型。
func (r *Registry) List() []Config {
	result := []Config{r.Resolve("explore"), r.Resolve("plan"), r.Resolve("general")}
	for _, item := range r.custom() {
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

// Descriptions 生成系统提示词中的自定义 Agent 清单。
func (r *Registry) Descriptions() string {
	custom := r.custom()
	if len(custom) == 0 {
		return ""
	}
	lines := []string{"Custom agent types:"}
	for _, item := range custom {
		lines = append(lines, fmt.Sprintf("- %s: %s", item.Name, item.Description))
	}
	sort.Strings(lines[1:])
	return strings.Join(lines, "\n")
}

// Refresh 清空自定义 Agent 缓存。
func (r *Registry) Refresh() { r.mu.Lock(); r.cache, r.loaded = nil, false; r.mu.Unlock() }

func (r *Registry) custom() map[string]Config {
	r.mu.RLock()
	if r.loaded {
		result := cloneMap(r.cache)
		r.mu.RUnlock()
		return result
	}
	r.mu.RUnlock()
	result := map[string]Config{}
	loadAgents(filepath.Join(r.home, ".claude", "agents"), result)
	loadAgents(filepath.Join(r.cwd, ".claude", "agents"), result)
	r.mu.Lock()
	r.cache, r.loaded = cloneMap(result), true
	r.mu.Unlock()
	return result
}

func loadAgents(dir string, result map[string]Config) {
	files, err := filepath.Glob(filepath.Join(dir, "*.md"))
	if err != nil {
		return
	}
	for _, path := range files {
		content, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		document, err := frontmatter.Parse(string(content))
		if err != nil {
			continue
		}
		name := frontmatter.String(document.Meta, "name")
		if name == "" {
			name = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		}
		result[name] = Config{Name: name, Description: frontmatter.String(document.Meta, "description"), SystemPrompt: document.Body, AllowedTools: frontmatter.Strings(document.Meta, "allowed-tools")}
	}
}

func cloneMap(source map[string]Config) map[string]Config {
	result := make(map[string]Config, len(source))
	for key, value := range source {
		value.AllowedTools = append([]string(nil), value.AllowedTools...)
		result[key] = value
	}
	return result
}
