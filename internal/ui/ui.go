package ui

import (
	"fmt"
	"io"
)

func PrintWelcome(w io.Writer) {
	fmt.Fprintln(w, "Mini Claude Go Version")
	fmt.Fprintln(w, "Type your request, or 'exit' to quit.")
	fmt.Fprintln(w, "Commands: /clear /cost")
}

func PrintPrompt(w io.Writer) {
	fprint(w, "\n> ")
}

func PrintAssistantText(w io.Writer, text string) {
	fmt.Fprintln(w, "\nassistant:", text)
}

func PrintInfo(w io.Writer, text string) {
	fmt.Fprintln(w, text)
}

func PrintError(w io.Writer, err error) {
	fmt.Fprintln(w, "error:", err)
}

func PrintCost(w io.Writer, inputTokens, outputTokens int) {
	fmt.Fprintf(w, "cost: input=%d output=%d\n", inputTokens, outputTokens)
}

func fprint(w io.Writer, text string) {
	_, _ = fmt.Fprint(w, text)
}
