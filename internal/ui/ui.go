// Package ui 提供终端感知的输出、spinner、工具摘要和差异预览。
package ui

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/term"

	"mini-claude/internal/agent"
)

const (
	ansiReset  = "\x1b[0m"
	ansiGray   = "\x1b[90m"
	ansiRed    = "\x1b[31m"
	ansiGreen  = "\x1b[32m"
	ansiYellow = "\x1b[33m"
	ansiCyan   = "\x1b[36m"
	ansiBold   = "\x1b[1m"
)

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// Renderer 把 Agent 事件渲染到终端；重定向输出时自动禁用 ANSI 与动画。
type Renderer struct {
	output io.Writer
	tty    bool

	mu          sync.Mutex
	spinnerStop chan struct{}
	spinnerDone chan struct{}
}

// NewRenderer 创建终端渲染器。
func NewRenderer(output io.Writer) *Renderer {
	return &Renderer{output: output, tty: IsTTY(output)}
}

// IsTTY 判断输出是否连接到交互终端。
func IsTTY(writer io.Writer) bool {
	file, ok := writer.(*os.File)
	return ok && term.IsTerminal(int(file.Fd()))
}

// HandleAgentEvent 渲染模型等待、工具调用和工具结果事件。
func (r *Renderer) HandleAgentEvent(event agent.Event) {
	switch event.Type {
	case agent.EventModelStart:
		r.startSpinner("Thinking")
	case agent.EventModelOutput:
		r.stopSpinner()
	case agent.EventToolStart:
		r.stopSpinner()
		r.printToolStart(event.ToolName, event.Arguments)
	case agent.EventToolResult:
		r.printToolResult(event.ToolName, event.Result, event.IsError)
	}
}

// Close 停止仍在运行的动画。
func (r *Renderer) Close() {
	r.stopSpinner()
}

func (r *Renderer) startSpinner(label string) {
	if !r.tty {
		return
	}
	r.mu.Lock()
	if r.spinnerStop != nil {
		r.mu.Unlock()
		return
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	r.spinnerStop, r.spinnerDone = stop, done
	r.mu.Unlock()
	go func() {
		defer close(done)
		ticker := time.NewTicker(80 * time.Millisecond)
		defer ticker.Stop()
		index := 0
		for {
			r.mu.Lock()
			fmt.Fprintf(r.output, "\r%s  %s %s...%s", ansiGray, spinnerFrames[index], label, ansiReset)
			r.mu.Unlock()
			select {
			case <-stop:
				r.mu.Lock()
				fmt.Fprint(r.output, "\r\x1b[K")
				r.mu.Unlock()
				return
			case <-ticker.C:
				index = (index + 1) % len(spinnerFrames)
			}
		}
	}()
}

func (r *Renderer) stopSpinner() {
	r.mu.Lock()
	stop, done := r.spinnerStop, r.spinnerDone
	if stop == nil {
		r.mu.Unlock()
		return
	}
	r.spinnerStop, r.spinnerDone = nil, nil
	close(stop)
	r.mu.Unlock()
	<-done
}

func (r *Renderer) printToolStart(name string, raw json.RawMessage) {
	var input map[string]any
	_ = json.Unmarshal(raw, &input)
	summary := toolSummary(name, input)
	line := "\n  [tool] " + name
	if summary != "" {
		line += " " + summary
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	fmt.Fprintln(r.output, r.color(ansiYellow, line))
}

func (r *Renderer) printToolResult(name, result string, failed bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if failed {
		fmt.Fprintln(r.output, r.color(ansiRed, indent(limit(result, 500))))
		return
	}
	lines := strings.Split(result, "\n")
	if len(lines) > 40 {
		lines = append(lines[:40], fmt.Sprintf("... (%d more lines)", len(lines)-40))
	}
	for _, line := range lines {
		color := ansiGray
		if name == "edit_file" || name == "write_file" {
			switch {
			case strings.HasPrefix(line, "+ "):
				color = ansiGreen
			case strings.HasPrefix(line, "- "):
				color = ansiRed
			case strings.HasPrefix(line, "@@"):
				color = ansiCyan
			}
		}
		fmt.Fprintln(r.output, r.color(color, "  "+limit(line, 1000)))
	}
}

func (r *Renderer) color(code, value string) string {
	if !r.tty {
		return value
	}
	return code + value + ansiReset
}

func toolSummary(name string, input map[string]any) string {
	stringAt := func(key string) string { value, _ := input[key].(string); return value }
	switch name {
	case "read_file", "write_file", "edit_file":
		return stringAt("file_path")
	case "list_files":
		return stringAt("pattern")
	case "grep_search":
		return fmt.Sprintf("%q in %s", stringAt("pattern"), first(stringAt("path"), "."))
	case "run_shell":
		return limit(stringAt("command"), 60)
	default:
		return ""
	}
}

func first(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func limit(value string, maximum int) string {
	if len(value) <= maximum {
		return value
	}
	return value[:maximum] + "..."
}

func indent(value string) string {
	return "  " + strings.ReplaceAll(value, "\n", "\n  ")
}

// PrintWelcome 输出 REPL 欢迎信息。
func PrintWelcome(w io.Writer) {
	fmt.Fprintln(w, "Mini Claude Go Version")
	fmt.Fprintln(w, "Type your request, or 'exit' to quit.")
	fmt.Fprintln(w, "Commands: /clear /plan /cost /compact /memory /skills")
}

// PrintPrompt 输出用户输入提示符。
func PrintPrompt(w io.Writer) { _, _ = fmt.Fprint(w, "\n> ") }

// PrintAssistantText 输出非流式助手文本。
func PrintAssistantText(w io.Writer, text string) { fmt.Fprintln(w, "\nassistant:", text) }

// PrintInfo 输出普通提示。
func PrintInfo(w io.Writer, text string) { fmt.Fprintln(w, text) }

// PrintError 输出错误。
func PrintError(w io.Writer, err error) { fmt.Fprintln(w, "error:", err) }

// PrintCost 输出简单 token 统计。
func PrintCost(w io.Writer, inputTokens, outputTokens int) {
	fmt.Fprintf(w, "cost: input=%d output=%d\n", inputTokens, outputTokens)
}
