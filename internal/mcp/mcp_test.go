package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mini-claude/internal/llm"
	"mini-claude/internal/tools"
)

func TestMCPHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_MCP_HELPER") != "1" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var request struct {
			ID     *int64         `json:"id"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil || request.ID == nil {
			continue
		}
		var result any
		switch request.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{}, "serverInfo": map[string]any{"name": "fixture", "version": "1"}}
		case "tools/list":
			result = map[string]any{"tools": []any{map[string]any{"name": "echo", "description": "Echo text", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}}}}}
		case "tools/call":
			if request.Params["name"] == "hang" {
				continue
			}
			arguments, _ := request.Params["arguments"].(map[string]any)
			result = map[string]any{"content": []any{map[string]any{"type": "text", "text": fmt.Sprint(arguments["text"])}}}
		default:
			encoded, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": *request.ID, "error": map[string]any{"code": -32601, "message": "not found"}})
			fmt.Println(string(encoded))
			continue
		}
		encoded, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": *request.ID, "result": result})
		fmt.Println(string(encoded))
	}
}

func TestConnectDiscoverRegisterAndCall(t *testing.T) {
	manager := fixtureManager(t, time.Second)
	if err := manager.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	registry := tools.NewRegistry(t.TempDir())
	manager.RegisterTools(registry)
	definitions := registry.Definitions()
	if len(definitions) != 1 || definitions[0].Name != "mcp__fixture__echo" {
		t.Fatalf("definitions=%#v", definitions)
	}
	result, err := registry.Execute(context.Background(), llm.ToolCall{Name: "mcp__fixture__echo", Arguments: json.RawMessage(`{"text":"hello MCP"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if result != "hello MCP" {
		t.Fatalf("result=%q", result)
	}
}

func TestRequestTimeout(t *testing.T) {
	manager := fixtureManager(t, 100*time.Millisecond)
	if err := manager.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	_, err := manager.callTool(context.Background(), "fixture", "hang", map[string]any{})
	if err == nil || !strings.Contains(err.Error(), "deadline exceeded") {
		t.Fatalf("err=%v", err)
	}
}

func TestLoadConfigsUsesExpectedPrecedence(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	write := func(path, command string) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		content := `{"mcpServers":{"same":{"command":"` + command + `"}}}`
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(home, ".claude", "settings.json"), "user")
	write(filepath.Join(cwd, ".claude", "settings.json"), "project")
	write(filepath.Join(cwd, ".mcp.json"), "mcp-json")
	configs, err := LoadConfigs(home, cwd)
	if err != nil {
		t.Fatal(err)
	}
	if configs["same"].Command != "mcp-json" {
		t.Fatalf("configs=%#v", configs)
	}
}

func fixtureManager(t *testing.T, timeout time.Duration) *Manager {
	t.Helper()
	return NewManagerWithConfigs(map[string]ServerConfig{"fixture": {
		Command: os.Args[0], Args: []string{"-test.run=TestMCPHelperProcess"},
		Env: map[string]string{"GO_WANT_MCP_HELPER": "1"},
	}}, timeout)
}
