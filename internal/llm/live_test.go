package llm

import (
	"context"
	"os"
	"testing"
	"time"
)

// 实时模型测试默认禁用，避免日常测试产生费用或依赖网络。
// 显式设置 RUN_LIVE_LLM_TESTS=1 后，还必须分别提供 API Key 和模型名。
func TestLiveOpenAI(t *testing.T) {
	if os.Getenv("RUN_LIVE_LLM_TESTS") != "1" {
		t.Skip("set RUN_LIVE_LLM_TESTS=1 to run live model tests")
	}
	key, model := os.Getenv("OPENAI_API_KEY"), os.Getenv("OPENAI_MODEL")
	if key == "" || model == "" {
		t.Fatal("OPENAI_API_KEY and OPENAI_MODEL are required for the live OpenAI test")
	}
	client := NewOpenAIClient(OpenAIOptions{APIKey: key, BaseURL: os.Getenv("OPENAI_BASE_URL")})
	assertLiveText(t, client, model)
}

func TestLiveAnthropic(t *testing.T) {
	if os.Getenv("RUN_LIVE_LLM_TESTS") != "1" {
		t.Skip("set RUN_LIVE_LLM_TESTS=1 to run live model tests")
	}
	key, model := os.Getenv("ANTHROPIC_API_KEY"), os.Getenv("ANTHROPIC_MODEL")
	if key == "" || model == "" {
		t.Fatal("ANTHROPIC_API_KEY and ANTHROPIC_MODEL are required for the live Anthropic test")
	}
	client := NewAnthropicClient(AnthropicOptions{APIKey: key, BaseURL: os.Getenv("ANTHROPIC_BASE_URL")})
	assertLiveText(t, client, model)
}

func assertLiveText(t *testing.T, client Client, model string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	response, err := client.Complete(ctx, Request{
		Model: model, Messages: []Message{TextMessage(RoleUser, "Reply with exactly: OK")}, MaxTokens: 16,
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Text == "" {
		t.Fatal("live model returned no text")
	}
}
