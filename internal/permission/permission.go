package permission

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Mode 是工具执行的权限模式。
type Mode string

const (
	ModeDefault           Mode = "default"
	ModePlan              Mode = "plan"
	ModeAcceptEdits       Mode = "acceptEdits"
	ModeBypassPermissions Mode = "bypassPermissions"
	ModeDontAsk           Mode = "dontAsk"
)

// Action 是一次权限判断的结果。
type Action string

const (
	ActionAllow   Action = "allow"
	ActionDeny    Action = "deny"
	ActionConfirm Action = "confirm"
)

// Decision 包含权限动作及给用户看的原因。
type Decision struct {
	Action  Action
	Message string
}

type rule struct {
	Tool    string
	Pattern string
}

type settings struct {
	Permissions struct {
		Allow []string `json:"allow"`
		Deny  []string `json:"deny"`
	} `json:"permissions"`
}

// Evaluator 根据模式、配置规则和危险操作特征判断工具权限。
type Evaluator struct {
	Mode         Mode
	PlanFilePath string
	CWD          string
	HomeDir      string
	allow        []rule
	deny         []rule
}

var dangerousPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\brm\s`),
	regexp.MustCompile(`(?i)\bgit\s+(push|reset|clean|checkout\s+\.)`),
	regexp.MustCompile(`(?i)\bsudo\b|\bmkfs\b|\bdd\s|>\s*/dev/`),
	regexp.MustCompile(`(?i)\bkill\b|\bpkill\b|\breboot\b|\bshutdown\b`),
	regexp.MustCompile(`(?i)\bdel\s|\brmdir\s|\bformat\s|\btaskkill\b`),
	regexp.MustCompile(`(?i)\bRemove-Item\b|\bStop-Process\b`),
}

var readTools = map[string]bool{
	"read_file": true, "list_files": true, "grep_search": true, "web_fetch": true, "tool_search": true,
}

// New 创建权限判断器并加载用户级、项目级设置。
func New(mode Mode, cwd, home string) *Evaluator {
	evaluator := &Evaluator{Mode: mode, CWD: cwd, HomeDir: home}
	evaluator.load(filepath.Join(home, ".claude", "settings.json"))
	evaluator.load(filepath.Join(cwd, ".claude", "settings.json"))
	return evaluator
}

// Check 对工具和已解析参数执行统一权限判断。
func (e *Evaluator) Check(tool string, input map[string]any) Decision {
	if e.Mode == ModeBypassPermissions {
		return Decision{Action: ActionAllow}
	}
	value := inputValue(tool, input)
	if matchRules(e.deny, tool, value) {
		return Decision{Action: ActionDeny, Message: "denied by permission rule"}
	}
	if matchRules(e.allow, tool, value) {
		return Decision{Action: ActionAllow}
	}
	if readTools[tool] {
		return Decision{Action: ActionAllow}
	}
	if tool == "enter_plan_mode" || tool == "exit_plan_mode" {
		return Decision{Action: ActionAllow}
	}
	if e.Mode == ModePlan {
		if (tool == "write_file" || tool == "edit_file") && samePath(value, e.PlanFilePath) {
			return Decision{Action: ActionAllow}
		}
		return Decision{Action: ActionDeny, Message: "blocked in plan mode"}
	}
	if e.Mode == ModeAcceptEdits && (tool == "write_file" || tool == "edit_file") {
		return Decision{Action: ActionAllow}
	}

	message := ""
	if tool == "run_shell" && IsDangerous(value) {
		message = value
	} else if (tool == "write_file" || tool == "edit_file") && value != "" {
		if _, err := os.Stat(value); os.IsNotExist(err) {
			message = "write new file: " + value
		}
	}
	if message == "" {
		return Decision{Action: ActionAllow}
	}
	if e.Mode == ModeDontAsk {
		return Decision{Action: ActionDeny, Message: "auto-denied: " + message}
	}
	return Decision{Action: ActionConfirm, Message: message}
}

// IsDangerous 检查命令是否命中跨平台危险操作模式。
func IsDangerous(command string) bool {
	for _, pattern := range dangerousPatterns {
		if pattern.MatchString(command) {
			return true
		}
	}
	return false
}

func (e *Evaluator) load(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var parsed settings
	if json.Unmarshal(data, &parsed) != nil {
		return
	}
	for _, item := range parsed.Permissions.Allow {
		e.allow = append(e.allow, parseRule(item))
	}
	for _, item := range parsed.Permissions.Deny {
		e.deny = append(e.deny, parseRule(item))
	}
}

func parseRule(value string) rule {
	if index := strings.IndexByte(value, '('); index > 0 && strings.HasSuffix(value, ")") {
		return rule{Tool: value[:index], Pattern: value[index+1 : len(value)-1]}
	}
	return rule{Tool: value}
}

func matchRules(rules []rule, tool, value string) bool {
	for _, item := range rules {
		if item.Tool != tool {
			continue
		}
		if item.Pattern == "" || item.Pattern == value {
			return true
		}
		if strings.HasSuffix(item.Pattern, "*") && strings.HasPrefix(value, strings.TrimSuffix(item.Pattern, "*")) {
			return true
		}
	}
	return false
}

func inputValue(tool string, input map[string]any) string {
	if tool == "run_shell" {
		value, _ := input["command"].(string)
		return value
	}
	value, _ := input["file_path"].(string)
	return value
}

func samePath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	absA, errA := filepath.Abs(a)
	absB, errB := filepath.Abs(b)
	return errA == nil && errB == nil && filepath.Clean(absA) == filepath.Clean(absB)
}
