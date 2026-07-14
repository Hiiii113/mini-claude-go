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
type abortable interface {
	Abort()
	IsProcessing() bool
}

// RunRepl 在标准终端运行 REPL，并实现处理中取消及空闲双 Ctrl+C 退出。
func RunRepl(agent Agent) error {
	interrupts := make(chan os.Signal, 2)
	signal.Notify(interrupts, os.Interrupt)
	defer signal.Stop(interrupts)
	go func() {
		idleInterrupts := 0
		var lastIdleInterrupt time.Time
		for range interrupts {
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
				os.Exit(0)
			}
			fmt.Fprintln(os.Stdout, "\n  Press Ctrl+C again to exit.")
		}
	}()
	return Run(os.Stdin, os.Stdout, agent)
}

// Run 使用指定输入输出运行可测试的 REPL。
func Run(input io.Reader, output io.Writer, agent Agent) error {
	reader := bufio.NewReader(input)
	if target, ok := agent.(confirmable); ok {
		target.SetConfirm(confirmer(reader, output))
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
			ui.PrintInfo(output, "No memories saved yet.")
		case "/skills":
			ui.PrintInfo(output, "No skills found. Add skills to .claude/skills/<name>/SKILL.md")
		default:
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
