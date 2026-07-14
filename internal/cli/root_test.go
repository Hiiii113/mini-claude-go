package cli

import (
	"context"
	"testing"

	"mini-claude/internal/config"
)

func TestRootCommandPassesCompatibleFlags(t *testing.T) {
	var got config.Options
	command := NewRootCommand(func(key string) string {
		if key == "ANTHROPIC_API_KEY" {
			return "test-key"
		}
		return ""
	}, func(_ context.Context, options config.Options) error {
		got = options
		return nil
	})
	command.SetArgs([]string{"--accept-edits", "--thinking", "-m", "test-model", "hello", "world"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if got.PermissionMode != "acceptEdits" || !got.Thinking || got.Model != "test-model" || got.Prompt != "hello world" || got.Provider != "anthropic" {
		t.Fatalf("options = %#v", got)
	}
}

func TestExplicitAPIBaseSelectsOpenAIProtocol(t *testing.T) {
	var provider string
	command := NewRootCommand(func(key string) string {
		if key == "ANTHROPIC_API_KEY" {
			return "test-key"
		}
		return ""
	}, func(_ context.Context, options config.Options) error { provider = options.Provider; return nil })
	command.SetArgs([]string{"--api-base", "https://proxy.test/v1"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if provider != "openai" {
		t.Fatalf("provider = %q", provider)
	}
}
