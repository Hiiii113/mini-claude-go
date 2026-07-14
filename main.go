package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"

	"github.com/joho/godotenv"

	agentpkg "mini-claude/internal/agent"
	"mini-claude/internal/cli"
	"mini-claude/internal/config"
	"mini-claude/internal/llm"
	"mini-claude/internal/prompt"
	"mini-claude/internal/session"
	"mini-claude/internal/ui"
)

func main() {
	if err := execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func execute() error {
	if err := godotenv.Load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("load .env: %w", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	command := cli.NewRootCommand(os.Getenv, runConfigured)
	command.SetContext(ctx)
	return command.Execute()
}

func runConfigured(ctx context.Context, options config.Options) error {
	if options.APIKey == "" {
		return errors.New("请设置 OPENAI_API_KEY、ANTHROPIC_API_KEY 或兼容变量 API_KEY")
	}
	var client llm.Client
	if options.Provider == "anthropic" {
		client = llm.NewAnthropicClient(llm.AnthropicOptions{APIKey: options.APIKey, BaseURL: options.APIBase})
	} else {
		client = llm.NewOpenAIClient(llm.OpenAIOptions{APIKey: options.APIKey, BaseURL: options.APIBase})
	}
	systemPrompt, err := prompt.Build(ctx, prompt.Options{})
	if err != nil {
		return fmt.Errorf("build system prompt: %w", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve home directory: %w", err)
	}
	renderer := ui.NewRenderer(os.Stdout)
	defer renderer.Close()
	store := session.NewStore(home)
	a := agentpkg.New(agentpkg.Options{
		Model: options.Model, PermissionMode: options.PermissionMode, Client: client,
		SystemPrompt: systemPrompt, Thinking: options.Thinking, MaxCostUSD: options.MaxCost,
		MaxTurns: options.MaxTurns, HomeDir: home, SessionStore: store, Output: os.Stdout,
		Events: renderer.HandleAgentEvent,
	})
	if options.Resume {
		data, loadErr := store.Latest()
		if loadErr == nil {
			a.RestoreSession(data)
			ui.PrintInfo(os.Stdout, fmt.Sprintf("Session restored (%d messages).", len(data.Messages)))
		} else if errors.Is(loadErr, os.ErrNotExist) {
			ui.PrintInfo(os.Stdout, "No previous sessions found.")
		} else {
			return fmt.Errorf("resume session: %w", loadErr)
		}
	}
	if options.Prompt != "" {
		a.SetConfirm(cli.NewConfirmer(os.Stdin, os.Stdout))
		return a.ChatContext(ctx, options.Prompt)
	}
	return cli.RunRepl(a)
}
