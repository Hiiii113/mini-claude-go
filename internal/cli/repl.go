package cli

import (
	"bufio"
	"io"
	"os"
	"strings"

	"mini-claude/internal/ui"
)

type Agent interface {
	Chat(input string) error
	ClearHistory()
	ShowCost()
}

func RunRepl(agent Agent) error {
	return Run(os.Stdin, os.Stdout, agent)
}

func Run(input io.Reader, output io.Writer, agent Agent) error {
	scanner := bufio.NewScanner(input)
	ui.PrintWelcome(output)

	for {
		ui.PrintPrompt(output)

		if !scanner.Scan() {
			return scanner.Err()
		}

		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		switch line {
		case "exit", "quit":
			ui.PrintInfo(output, "Bye!")
			return nil
		case "/clear":
			agent.ClearHistory()
			continue
		case "/cost":
			agent.ShowCost()
			continue
		}

		if err := agent.Chat(line); err != nil {
			ui.PrintError(output, err)
		}
	}
}
