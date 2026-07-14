// Package config 合并环境变量和命令行参数，并保持旧配置兼容。
package config

import (
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

const (
	defaultOpenAIModel    = "gpt-4.1-mini"
	defaultAnthropicModel = "claude-sonnet-4-5"
)

// Options 保存命令行与环境变量合并后的运行配置。
type Options struct {
	PermissionMode string
	Provider       string
	APIKey         string
	Model          string
	APIBase        string
	Prompt         string
	Resume         bool
	Thinking       bool
	MaxCost        float64
	MaxTurns       int
}

// ModeFlags 保存互斥权限模式开关；Apply 按兼容优先级计算最终模式。
type ModeFlags struct {
	Yolo, Plan, AcceptEdits, DontAsk bool
}

// FromEnvironment 按 OpenAI、Anthropic、旧版通用变量的顺序选择后端。
func FromEnvironment(getenv func(string) string) Options {
	opt := Options{PermissionMode: "default", Provider: "openai"}
	switch {
	case getenv("OPENAI_API_KEY") != "":
		opt.APIKey = getenv("OPENAI_API_KEY")
		opt.APIBase = getenv("OPENAI_BASE_URL")
		opt.Model = firstNonEmpty(getenv("OPENAI_MODEL"), getenv("MINI_CLAUDE_MODEL"), getenv("MODEL_NAME"), defaultOpenAIModel)
	case getenv("ANTHROPIC_API_KEY") != "":
		opt.Provider = "anthropic"
		opt.APIKey = getenv("ANTHROPIC_API_KEY")
		opt.APIBase = getenv("ANTHROPIC_BASE_URL")
		opt.Model = firstNonEmpty(getenv("ANTHROPIC_MODEL"), getenv("MINI_CLAUDE_MODEL"), getenv("MODEL_NAME"), defaultAnthropicModel)
	default:
		// 兼容早期版本的 API_KEY/BASE_URL/MODEL_NAME，但优先级最低。
		opt.APIKey = getenv("API_KEY")
		opt.APIBase = getenv("BASE_URL")
		opt.Model = firstNonEmpty(getenv("MINI_CLAUDE_MODEL"), getenv("MODEL_NAME"), defaultOpenAIModel)
	}
	return opt
}

// BindFlags 把兼容参数绑定到 Cobra 命令。
func BindFlags(command *cobra.Command, options *Options) *ModeFlags {
	modes := &ModeFlags{}
	flags := command.Flags()
	flags.BoolVarP(&modes.Yolo, "yolo", "y", false, "跳过所有确认")
	flags.BoolVar(&modes.Plan, "plan", false, "只读计划模式")
	flags.BoolVar(&modes.AcceptEdits, "accept-edits", false, "自动允许文件编辑")
	flags.BoolVar(&modes.DontAsk, "dont-ask", false, "自动拒绝需要确认的操作")
	flags.BoolVar(&options.Thinking, "thinking", false, "启用扩展思考")
	flags.BoolVar(&options.Resume, "resume", false, "恢复最近会话")
	flags.Float64Var(&options.MaxCost, "max-cost", 0, "最大估算费用（USD）")
	flags.IntVar(&options.MaxTurns, "max-turns", 0, "最大 Agent 轮数")
	flags.StringVarP(&options.Model, "model", "m", options.Model, "模型名称")
	flags.StringVar(&options.APIBase, "api-base", options.APIBase, "自定义模型 API 地址")
	return modes
}

// Apply 把权限模式开关写回 Options。
func (m *ModeFlags) Apply(options *Options) {
	switch {
	case m.DontAsk:
		options.PermissionMode = "dontAsk"
	case m.AcceptEdits:
		options.PermissionMode = "acceptEdits"
	case m.Plan:
		options.PermissionMode = "plan"
	case m.Yolo:
		options.PermissionMode = "bypassPermissions"
	}
}

// Parse 是供测试和嵌入调用的 Cobra 参数解析入口。
func Parse(args []string, getenv func(string) string) (Options, error) {
	options := FromEnvironment(getenv)
	command := &cobra.Command{
		Use:           "mini-claude [prompt]",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.ArbitraryArgs,
	}
	modes := BindFlags(command, &options)
	command.RunE = func(_ *cobra.Command, positional []string) error {
		modes.Apply(&options)
		options.Prompt = strings.Join(positional, " ")
		return nil
	}
	command.SetArgs(args)
	command.SetOut(io.Discard)
	command.SetErr(io.Discard)
	return options, command.Execute()
}

// ParseArgs 解析当前进程参数。
func ParseArgs() (Options, error) { return Parse(os.Args[1:], os.Getenv) }

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
