package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"sort"

	"github.com/joho/godotenv"

	agentpkg "mini-claude/internal/agent"
	"mini-claude/internal/cli"
	"mini-claude/internal/config"
	"mini-claude/internal/llm"
	"mini-claude/internal/mcp"
	"mini-claude/internal/memory"
	"mini-claude/internal/prompt"
	"mini-claude/internal/session"
	"mini-claude/internal/skills"
	"mini-claude/internal/subagent"
	"mini-claude/internal/tools"
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
	command := cli.NewRootCommand(os.Getenv, runConfigured)
	command.SetContext(context.Background())
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
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve home directory: %w", err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve working directory: %w", err)
	}
	runCtx := ctx
	stop := func() {}
	if options.Prompt != "" {
		runCtx, stop = signal.NotifyContext(ctx, os.Interrupt)
	}
	defer stop()
	toolRegistry := tools.DefaultRegistry(cwd)
	mcpManager := mcp.NewManager(home, cwd)
	defer mcpManager.Close()
	if connectErr := mcpManager.Connect(runCtx); connectErr != nil {
		fmt.Fprintln(os.Stderr, "[mcp] warning:", connectErr)
	}
	mcpManager.RegisterTools(toolRegistry)
	counts := map[string]int{}
	for _, item := range mcpManager.Tools() {
		counts[item.Server]++
	}
	servers := make([]string, 0, len(counts))
	for server := range counts {
		servers = append(servers, server)
	}
	sort.Strings(servers)
	for _, server := range servers {
		fmt.Fprintf(os.Stderr, "[mcp] Connected to %q — %d tools\n", server, counts[server])
	}
	memoryStore := memory.NewStore(home, cwd)
	skillRegistry := skills.NewRegistry(home, cwd)
	subAgentRegistry := subagent.NewRegistry(home, cwd)
	systemPrompt, err := prompt.Build(runCtx, prompt.Options{
		WorkingDir: cwd, Memory: memoryStore.PromptSection(), Skills: skillRegistry.Descriptions(),
		Agents: subAgentRegistry.Descriptions(), DeferredTools: []string{"enter_plan_mode", "exit_plan_mode"},
	})
	if err != nil {
		return fmt.Errorf("build system prompt: %w", err)
	}
	renderer := ui.NewRenderer(os.Stdout)
	defer renderer.Close()
	store := session.NewStore(home)
	a := agentpkg.New(agentpkg.Options{
		Model: options.Model, PermissionMode: options.PermissionMode, Client: client,
		Tools:        toolRegistry,
		SystemPrompt: systemPrompt, Thinking: options.Thinking, MaxCostUSD: options.MaxCost,
		MaxTurns: options.MaxTurns, HomeDir: home, SessionStore: store, Output: os.Stdout,
		Events: renderer.HandleAgentEvent, WorkingDir: cwd, Memories: memoryStore,
		Skills: skillRegistry, SubAgents: subAgentRegistry,
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
		a.SetPlanApproval(cli.NewPlanApprover(os.Stdin, os.Stdout))
		return a.ChatContext(runCtx, options.Prompt)
	}
	return cli.RunRepl(a)
}
