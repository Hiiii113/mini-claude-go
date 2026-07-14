package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/joho/godotenv"

	agentpkg "mini-claude/internal/agent"
	"mini-claude/internal/cli"
	"mini-claude/internal/config"
	"mini-claude/internal/llm"
	"mini-claude/internal/prompt"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	// 加载.env文件
	if err := godotenv.Load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("load .env: %w", err)
	}

	// 加载配置opt
	opt, err := config.ParseArgs()
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("parse arguments: %w", err)
	}

	if opt.APIKey == "" {
		return errors.New("请设置 OPENAI_API_KEY、ANTHROPIC_API_KEY 或兼容变量 API_KEY")
	}

	var client llm.Client
	if opt.Provider == "anthropic" {
		client = llm.NewAnthropicClient(llm.AnthropicOptions{APIKey: opt.APIKey, BaseURL: opt.APIBase})
	} else {
		client = llm.NewOpenAIClient(llm.OpenAIOptions{APIKey: opt.APIKey, BaseURL: opt.APIBase})
	}
	systemPrompt, err := prompt.Build(context.Background(), prompt.Options{})
	if err != nil {
		return fmt.Errorf("build system prompt: %w", err)
	}

	a := agentpkg.New(agentpkg.Options{
		Model:          opt.Model,
		PermissionMode: opt.PermissionMode,
		Client:         client,
		SystemPrompt:   systemPrompt,
		Thinking:       opt.Thinking,
		MaxCostUSD:     opt.MaxCost,
		MaxTurns:       opt.MaxTurns,
	})

	if opt.Prompt != "" {
		return a.Chat(opt.Prompt)
	}

	return cli.RunRepl(a)
}
