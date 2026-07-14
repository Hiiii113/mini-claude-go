package config

import "testing"

func TestParseUsesEnvironmentDefaults(t *testing.T) {
	env := map[string]string{
		"API_KEY":    "legacy-key",
		"MODEL_NAME": "test-model",
		"BASE_URL":   "https://example.test/v1",
	}

	opt, err := Parse(nil, func(key string) string { return env[key] })
	if err != nil {
		t.Fatal(err)
	}
	if opt.Model != env["MODEL_NAME"] {
		t.Fatalf("Model = %q, want %q", opt.Model, env["MODEL_NAME"])
	}
	if opt.APIBase != env["BASE_URL"] {
		t.Fatalf("APIBase = %q, want %q", opt.APIBase, env["BASE_URL"])
	}
}

func TestOpenAITakesPriorityOverAnthropicAndLegacy(t *testing.T) {
	env := map[string]string{
		"OPENAI_API_KEY": "openai", "OPENAI_MODEL": "openai-model", "OPENAI_BASE_URL": "https://openai.test/v1",
		"ANTHROPIC_API_KEY": "anthropic", "API_KEY": "legacy",
	}
	opt, err := Parse(nil, func(key string) string { return env[key] })
	if err != nil {
		t.Fatal(err)
	}
	if opt.Provider != "openai" || opt.APIKey != "openai" || opt.Model != "openai-model" {
		t.Fatalf("unexpected options: %#v", opt)
	}
}

func TestAnthropicSelectedWithoutOpenAI(t *testing.T) {
	env := map[string]string{"ANTHROPIC_API_KEY": "anthropic", "ANTHROPIC_MODEL": "claude-test"}
	opt, err := Parse(nil, func(key string) string { return env[key] })
	if err != nil {
		t.Fatal(err)
	}
	if opt.Provider != "anthropic" || opt.Model != "claude-test" {
		t.Fatalf("unexpected options: %#v", opt)
	}
}

func TestParseFlagsOverrideEnvironment(t *testing.T) {
	args := []string{"--yolo", "--model", "cli-model", "--api-base", "https://cli.test/v1", "hello", "world"}
	opt, err := Parse(args, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if opt.PermissionMode != "bypassPermissions" {
		t.Fatalf("PermissionMode = %q", opt.PermissionMode)
	}
	if opt.Model != "cli-model" || opt.APIBase != "https://cli.test/v1" {
		t.Fatalf("unexpected model/API base: %#v", opt)
	}
	if opt.Prompt != "hello world" {
		t.Fatalf("Prompt = %q", opt.Prompt)
	}
}
