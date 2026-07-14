package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"time"

	agentpkg "mini-claude/internal/agent"
	"mini-claude/internal/ui"
)

// Agent 是 REPL 所需的最小 Agent 能力。
type Agent interface {
	Chat(input string) error
	ClearHistory()
	ShowCost()
}

type confirmable interface {
	SetConfirm(func(context.Context, string) (bool, error))
}

type compactable interface{ Compact() error }
type planToggle interface{ TogglePlanMode() string }
type planApprovable interface {
	SetPlanApproval(func(context.Context, string) (agentpkg.PlanDecision, error))
}
type memoryLister interface{ MemoryDescriptions() []string }
type skillLister interface{ SkillDescriptions() []string }
type skillInvoker interface{ InvokeSkill(string) (bool, error) }
type abortable interface {
	Abort()
	IsProcessing() bool
}

// RunRepl 在标准终端运行 REPL，并实现处理中取消及空闲双 Ctrl+C 退出。
func RunRepl(agent Agent) error {
	interrupts := make(chan os.Signal, 2)
	signal.Notify(interrupts, os.Interrupt)
	defer signal.Stop(interrupts)
	done := make(chan error, 1)
	go func() { done <- Run(os.Stdin, os.Stdout, agent) }()
	idleInterrupts := 0
	var lastIdleInterrupt time.Time
	for {
		select {
		case err := <-done:
			return err
		case <-interrupts:
			if target, ok := agent.(abortable); ok && target.IsProcessing() {
				target.Abort()
				fmt.Fprintln(os.Stdout, "\n  (interrupted)")
				idleInterrupts = 0
				continue
			}
			if time.Since(lastIdleInterrupt) > 2*time.Second {
				idleInterrupts = 0
			}
			idleInterrupts++
			lastIdleInterrupt = time.Now()
			if idleInterrupts >= 2 {
				fmt.Fprintln(os.Stdout, "\nBye!")
				return nil
			}
			fmt.Fprintln(os.Stdout, "\n  Press Ctrl+C again to exit.")
		}
	}
}

// Run 使用指定输入输出运行可测试的 REPL。
func Run(input io.Reader, output io.Writer, agent Agent) error {
	reader := bufio.NewReader(input)
	if target, ok := agent.(confirmable); ok {
		target.SetConfirm(confirmer(reader, output))
	}
	if target, ok := agent.(planApprovable); ok {
		target.SetPlanApproval(planApprover(reader, output))
	}
	ui.PrintWelcome(output)

	for {
		ui.PrintPrompt(output)
		line, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			if errors.Is(err, io.EOF) {
				return nil
			}
			continue
		}

		switch line {
		case "exit", "quit":
			ui.PrintInfo(output, "Bye!")
			return nil
		case "/clear":
			agent.ClearHistory()
		case "/cost":
			agent.ShowCost()
		case "/compact":
			if target, ok := agent.(compactable); ok {
				if compactErr := target.Compact(); compactErr != nil {
					ui.PrintError(output, compactErr)
				}
			} else {
				ui.PrintInfo(output, "Context compaction is unavailable.")
			}
		case "/plan":
			if target, ok := agent.(planToggle); ok {
				ui.PrintInfo(output, "Permission mode: "+target.TogglePlanMode())
			} else {
				ui.PrintInfo(output, "Plan mode is unavailable.")
			}
		case "/memory":
			items := []string(nil)
			if target, ok := agent.(memoryLister); ok {
				items = target.MemoryDescriptions()
			}
			if len(items) == 0 {
				ui.PrintInfo(output, "No memories saved yet.")
			} else {
				ui.PrintInfo(output, fmt.Sprintf("%d memories:", len(items)))
				for _, item := range items {
					fmt.Fprintln(output, "    "+item)
				}
			}
		case "/skills":
			items := []string(nil)
			if target, ok := agent.(skillLister); ok {
				items = target.SkillDescriptions()
			}
			if len(items) == 0 {
				ui.PrintInfo(output, "No skills found. Add skills to .claude/skills/<name>/SKILL.md")
			} else {
				ui.PrintInfo(output, fmt.Sprintf("%d skills:", len(items)))
				for _, item := range items {
					fmt.Fprintln(output, "    "+item)
				}
			}
		default:
			if strings.HasPrefix(line, "/") {
				if target, ok := agent.(skillInvoker); ok {
					handled, invokeErr := target.InvokeSkill(line)
					if invokeErr != nil {
						ui.PrintError(output, invokeErr)
					}
					if handled {
						break
					}
				}
			}
			if chatErr := agent.Chat(line); chatErr != nil && !errors.Is(chatErr, context.Canceled) {
				ui.PrintError(output, chatErr)
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
	}
}

// NewConfirmer 创建供 one-shot 模式复用的终端确认函数。
func NewConfirmer(input io.Reader, output io.Writer) func(context.Context, string) (bool, error) {
	return confirmer(bufio.NewReader(input), output)
}

// NewPlanApprover 创建 one-shot 和 REPL 共用的四选项计划审批函数。
func NewPlanApprover(input io.Reader, output io.Writer) func(context.Context, string) (agentpkg.PlanDecision, error) {
	return planApprover(bufio.NewReader(input), output)
}

func planApprover(reader *bufio.Reader, output io.Writer) func(context.Context, string) (agentpkg.PlanDecision, error) {
	return func(ctx context.Context, plan string) (agentpkg.PlanDecision, error) {
		fmt.Fprintln(output, "\n  ━━━ Plan for Approval ━━━")
		lines := strings.Split(plan, "\n")
		if len(lines) > 60 {
			lines = append(lines[:60], fmt.Sprintf("... (%d more lines)", len(lines)-60))
		}
		for _, line := range lines {
			fmt.Fprintln(output, "  "+line)
		}
		fmt.Fprintln(output, "  1) Clear context and execute\n  2) Execute and keep context\n  3) Execute with manual approvals\n  4) Keep planning")
		for {
			fmt.Fprint(output, "  Enter choice (1-4): ")
			answer, err := readLine(ctx, reader)
			if err != nil {
				return agentpkg.PlanDecision{}, err
			}
			switch strings.TrimSpace(answer) {
			case "1":
				return agentpkg.PlanDecision{Choice: agentpkg.PlanClearAndExecute}, nil
			case "2":
				return agentpkg.PlanDecision{Choice: agentpkg.PlanExecute}, nil
			case "3":
				return agentpkg.PlanDecision{Choice: agentpkg.PlanManualExecute}, nil
			case "4":
				fmt.Fprint(output, "  Feedback: ")
				feedback, readErr := readLine(ctx, reader)
				return agentpkg.PlanDecision{Choice: agentpkg.PlanKeepPlanning, Feedback: strings.TrimSpace(feedback)}, readErr
			default:
				fmt.Fprintln(output, "  Invalid choice.")
			}
		}
	}
}

func confirmer(reader *bufio.Reader, output io.Writer) func(context.Context, string) (bool, error) {
	return func(ctx context.Context, message string) (bool, error) {
		fmt.Fprintf(output, "\n  Permission required: %s\n  Allow? (y/n): ", message)
		answer, err := readLine(ctx, reader)
		if err != nil {
			return false, err
		}
		return strings.HasPrefix(strings.ToLower(strings.TrimSpace(answer)), "y"), nil
	}
}

func readLine(ctx context.Context, reader *bufio.Reader) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	value, err := reader.ReadString('\n')
	if contextErr := ctx.Err(); contextErr != nil {
		return "", contextErr
	}
	return value, err
}
