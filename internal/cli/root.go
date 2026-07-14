package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"mini-claude/internal/config"
)

// Runner 接收已合并配置并启动 one-shot 或 REPL。
type Runner func(context.Context, config.Options) error

// NewRootCommand 创建兼容原 TypeScript 参数的 Cobra 根命令。
func NewRootCommand(getenv func(string) string, runner Runner) *cobra.Command {
	options := config.FromEnvironment(getenv)
	command := &cobra.Command{
		Use:           "mini-claude [options] [prompt]",
		Short:         "跨平台 Go 编码 Agent",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.ArbitraryArgs,
		Long: `Mini Claude 是一个跨平台的命令行编码 Agent。

REPL commands:
  /clear    清空会话      /plan     切换计划模式
  /cost     查看费用      /compact  压缩上下文
  /memory   查看记忆      /skills   查看技能`,
	}
	modes := config.BindFlags(command, &options)
	command.RunE = func(command *cobra.Command, args []string) error {
		modes.Apply(&options)
		options.Prompt = strings.Join(args, " ")
		if options.MaxCost < 0 {
			return fmt.Errorf("--max-cost 不能为负数")
		}
		if options.MaxTurns < 0 {
			return fmt.Errorf("--max-turns 不能为负数")
		}
		// 与 TypeScript 兼容：显式 --api-base 表示 OpenAI-compatible 协议。
		if command.Flags().Changed("api-base") {
			options.Provider = "openai"
		}
		return runner(command.Context(), options)
	}
	return command
}
