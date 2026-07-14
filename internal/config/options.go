package config

import (
	"flag"
	"os"
	"strings"
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

func ParseArgs() (Options, error) {
	return Parse(os.Args[1:], os.Getenv)
}

func Parse(args []string, getenv func(string) string) (Options, error) {
	opt := resolveEnvironment(getenv)

	flags := flag.NewFlagSet("mini-claude", flag.ContinueOnError)
	var yolo bool
	var plan bool
	var acceptEdits bool
	var dontAsk bool

	flags.BoolVar(&yolo, "yolo", false, "skip confirmations")
	flags.BoolVar(&yolo, "y", false, "skip confirmations")
	flags.BoolVar(&plan, "plan", false, "plan mode")
	flags.BoolVar(&acceptEdits, "accept-edits", false, "auto approve file edits")
	flags.BoolVar(&dontAsk, "dont-ask", false, "auto deny confirmations")
	flags.BoolVar(&opt.Thinking, "thinking", false, "enable thinking")
	flags.BoolVar(&opt.Resume, "resume", false, "resume last session")
	flags.Float64Var(&opt.MaxCost, "max-cost", 0, "max cost in USD")
	flags.IntVar(&opt.MaxTurns, "max-turns", 0, "max turns")
	flags.StringVar(&opt.Model, "model", opt.Model, "model name")
	flags.StringVar(&opt.Model, "m", opt.Model, "model name")
	flags.StringVar(&opt.APIBase, "api-base", opt.APIBase, "OpenAI-compatible API base")

	if err := flags.Parse(args); err != nil {
		return Options{}, err
	}

	switch {
	case dontAsk:
		opt.PermissionMode = "dontAsk"
	case acceptEdits:
		opt.PermissionMode = "acceptEdits"
	case plan:
		opt.PermissionMode = "plan"
	case yolo:
		opt.PermissionMode = "bypassPermissions"
	}

	opt.Prompt = strings.Join(flags.Args(), " ")
	return opt, nil
}

// resolveEnvironment 按 OpenAI、Anthropic、旧版通用变量的顺序选择后端。
func resolveEnvironment(getenv func(string) string) Options {
	opt := Options{PermissionMode: "default", Provider: "openai"}
	switch {
	case getenv("OPENAI_API_KEY") != "":
		opt.APIKey = getenv("OPENAI_API_KEY")
		opt.APIBase = getenv("OPENAI_BASE_URL")
		opt.Model = firstNonEmpty(getenv("OPENAI_MODEL"), getenv("MODEL_NAME"), defaultOpenAIModel)
	case getenv("ANTHROPIC_API_KEY") != "":
		opt.Provider = "anthropic"
		opt.APIKey = getenv("ANTHROPIC_API_KEY")
		opt.APIBase = getenv("ANTHROPIC_BASE_URL")
		opt.Model = firstNonEmpty(getenv("ANTHROPIC_MODEL"), getenv("MODEL_NAME"), defaultAnthropicModel)
	default:
		// 兼容早期版本的 API_KEY/BASE_URL/MODEL_NAME，但优先级最低。
		opt.APIKey = getenv("API_KEY")
		opt.APIBase = getenv("BASE_URL")
		opt.Model = firstNonEmpty(getenv("MODEL_NAME"), defaultOpenAIModel)
	}
	return opt
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
