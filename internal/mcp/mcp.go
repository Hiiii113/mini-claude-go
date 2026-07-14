// Package mcp 实现 JSON-RPC 2.0 over stdio 的 MCP 客户端和动态工具注册。
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"mini-claude/internal/llm"
	"mini-claude/internal/tools"
)

const defaultTimeout = 15 * time.Second

// ServerConfig 描述一个 stdio MCP 服务器进程。
type ServerConfig struct {
	Command string            `json:"command"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

// Tool 是服务器发现的 MCP 工具。
type Tool struct {
	Name, Description, Server string
	InputSchema               map[string]any
}

type rpcError struct {
	Code    int64  `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *rpcError       `json:"error,omitempty"`
}

type connection struct {
	name, command string
	args          []string
	env           map[string]string
	timeout       time.Duration

	ctx    context.Context
	cancel context.CancelFunc
	cmd    *exec.Cmd
	stdin  io.WriteCloser

	writeMu sync.Mutex
	mu      sync.Mutex
	nextID  int64
	pending map[int64]chan rpcResponse
	closed  bool
	done    chan struct{}
}

// Manager 管理配置合并、连接、工具发现和关闭清理。
type Manager struct {
	home, cwd string
	timeout   time.Duration
	configs   map[string]ServerConfig

	mu          sync.RWMutex
	connections map[string]*connection
	tools       []Tool
	connected   bool
}

// NewManager 创建从用户设置、项目设置和 .mcp.json 加载配置的管理器。
func NewManager(home, cwd string) *Manager {
	return &Manager{home: home, cwd: cwd, timeout: defaultTimeout, connections: map[string]*connection{}}
}

// NewManagerWithConfigs 创建直接注入配置的管理器，主要用于测试和嵌入。
func NewManagerWithConfigs(configs map[string]ServerConfig, timeout time.Duration) *Manager {
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	return &Manager{configs: cloneConfigs(configs), timeout: timeout, connections: map[string]*connection{}}
}

// LoadConfigs 按用户设置、项目设置、.mcp.json 的顺序覆盖合并配置。
func LoadConfigs(home, cwd string) (map[string]ServerConfig, error) {
	result := map[string]ServerConfig{}
	paths := []string{
		filepath.Join(home, ".claude", "settings.json"),
		filepath.Join(cwd, ".claude", "settings.json"),
		filepath.Join(cwd, ".mcp.json"),
	}
	for _, path := range paths {
		content, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read MCP config %s: %w", path, err)
		}
		var envelope struct {
			Servers map[string]ServerConfig `json:"mcpServers"`
		}
		if err := json.Unmarshal(content, &envelope); err != nil {
			return nil, fmt.Errorf("parse MCP config %s: %w", path, err)
		}
		if envelope.Servers == nil {
			if err := json.Unmarshal(content, &envelope.Servers); err != nil {
				return nil, fmt.Errorf("parse MCP servers %s: %w", path, err)
			}
		}
		for name, config := range envelope.Servers {
			if strings.TrimSpace(name) == "" || strings.TrimSpace(config.Command) == "" {
				continue
			}
			result[name] = config
		}
	}
	return result, nil
}

// Connect 启动全部服务器并完成 initialize、initialized 和 tools/list。
// 单个服务器失败不会关闭已经成功的连接，返回值会汇总失败原因。
func (m *Manager) Connect(parent context.Context) error {
	m.mu.Lock()
	if m.connected {
		m.mu.Unlock()
		return nil
	}
	m.connected = true
	configs := cloneConfigs(m.configs)
	m.mu.Unlock()
	if configs == nil {
		var err error
		configs, err = LoadConfigs(m.home, m.cwd)
		if err != nil {
			return err
		}
	}
	names := make([]string, 0, len(configs))
	for name := range configs {
		names = append(names, name)
	}
	sort.Strings(names)
	var failures []error
	for _, name := range names {
		conn := newConnection(parent, name, configs[name], m.timeout)
		if err := conn.start(); err != nil {
			failures = append(failures, fmt.Errorf("MCP %s: %w", name, err))
			continue
		}
		if err := conn.initialize(parent); err != nil {
			conn.close()
			failures = append(failures, fmt.Errorf("MCP %s: %w", name, err))
			continue
		}
		serverTools, err := conn.listTools(parent)
		if err != nil {
			conn.close()
			failures = append(failures, fmt.Errorf("MCP %s: %w", name, err))
			continue
		}
		m.mu.Lock()
		m.connections[name] = conn
		m.tools = append(m.tools, serverTools...)
		m.mu.Unlock()
	}
	return errors.Join(failures...)
}

// Tools 返回发现工具的副本。
func (m *Manager) Tools() []Tool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := append([]Tool(nil), m.tools...)
	for index := range result {
		result[index].InputSchema = cloneSchema(result[index].InputSchema)
	}
	return result
}

// RegisterTools 把 MCP 工具以前缀名称注册到统一工具表。
func (m *Manager) RegisterTools(registry *tools.Registry) {
	for _, discovered := range m.Tools() {
		item := discovered
		name := "mcp__" + item.Server + "__" + item.Name
		description := item.Description
		if description == "" {
			description = fmt.Sprintf("MCP tool %s from %s", item.Name, item.Server)
		}
		schema := item.InputSchema
		if schema == nil {
			schema = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		registry.Register(llm.ToolDefinition{Name: name, Description: description, InputSchema: schema}, func(ctx context.Context, arguments json.RawMessage) (string, error) {
			var input any = map[string]any{}
			if len(arguments) > 0 {
				if err := json.Unmarshal(arguments, &input); err != nil {
					return "", fmt.Errorf("decode MCP arguments: %w", err)
				}
			}
			return m.callTool(ctx, item.Server, item.Name, input)
		}, false)
	}
}

// Close 关闭 stdin、取消所有进程并等待退出。
func (m *Manager) Close() {
	m.mu.Lock()
	connections := make([]*connection, 0, len(m.connections))
	for _, conn := range m.connections {
		connections = append(connections, conn)
	}
	m.connections = map[string]*connection{}
	m.tools = nil
	m.connected = false
	m.mu.Unlock()
	for _, conn := range connections {
		conn.close()
	}
}

func (m *Manager) callTool(ctx context.Context, server, name string, arguments any) (string, error) {
	m.mu.RLock()
	conn := m.connections[server]
	m.mu.RUnlock()
	if conn == nil {
		return "", fmt.Errorf("MCP server %q is not connected", server)
	}
	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := conn.request(ctx, "tools/call", map[string]any{"name": name, "arguments": arguments}, &result); err != nil {
		return "", err
	}
	parts := make([]string, 0, len(result.Content))
	for _, block := range result.Content {
		if block.Type == "text" {
			parts = append(parts, block.Text)
		}
	}
	text := strings.Join(parts, "\n")
	if result.IsError {
		return text, errors.New(firstNonEmpty(text, "MCP tool returned an error"))
	}
	if text == "" {
		encoded, _ := json.Marshal(result)
		text = string(encoded)
	}
	return text, nil
}

func newConnection(parent context.Context, name string, config ServerConfig, timeout time.Duration) *connection {
	ctx, cancel := context.WithCancel(parent)
	return &connection{name: name, command: config.Command, args: append([]string(nil), config.Args...), env: cloneEnv(config.Env), timeout: timeout, ctx: ctx, cancel: cancel, nextID: 1, pending: map[int64]chan rpcResponse{}, done: make(chan struct{})}
}

func (c *connection) start() error {
	c.cmd = exec.CommandContext(c.ctx, c.command, c.args...)
	c.cmd.Env = mergeEnv(os.Environ(), c.env)
	stdin, err := c.cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := c.cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := c.cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := c.cmd.Start(); err != nil {
		return err
	}
	c.stdin = stdin
	go io.Copy(io.Discard, stderr)
	go c.readLoop(stdout)
	go func() {
		err := c.cmd.Wait()
		c.failPending(fmt.Errorf("MCP server %q exited: %w", c.name, err))
		close(c.done)
	}()
	return nil
}

func (c *connection) readLoop(reader io.Reader) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var response rpcResponse
		if json.Unmarshal(scanner.Bytes(), &response) != nil || response.ID == 0 {
			continue
		}
		c.mu.Lock()
		pending := c.pending[response.ID]
		delete(c.pending, response.ID)
		c.mu.Unlock()
		if pending != nil {
			pending <- response
			close(pending)
		}
	}
	if err := scanner.Err(); err != nil {
		c.failPending(fmt.Errorf("read MCP %q stdout: %w", c.name, err))
	}
}

func (c *connection) initialize(ctx context.Context) error {
	var ignored map[string]any
	if err := c.request(ctx, "initialize", map[string]any{
		"protocolVersion": "2024-11-05", "capabilities": map[string]any{},
		"clientInfo": map[string]any{"name": "mini-claude", "version": "1.0.0"},
	}, &ignored); err != nil {
		return err
	}
	return c.notify("notifications/initialized", map[string]any{})
}

func (c *connection) listTools(ctx context.Context) ([]Tool, error) {
	var result struct {
		Tools []struct {
			Name        string         `json:"name"`
			Description string         `json:"description"`
			InputSchema map[string]any `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := c.request(ctx, "tools/list", map[string]any{}, &result); err != nil {
		return nil, err
	}
	tools := make([]Tool, 0, len(result.Tools))
	for _, item := range result.Tools {
		if item.Name != "" {
			tools = append(tools, Tool{Name: item.Name, Description: item.Description, InputSchema: item.InputSchema, Server: c.name})
		}
	}
	return tools, nil
}

func (c *connection) request(parent context.Context, method string, params any, target any) error {
	ctx, cancel := context.WithTimeout(parent, c.timeout)
	defer cancel()
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return fmt.Errorf("MCP server %q is closed", c.name)
	}
	id := c.nextID
	c.nextID++
	pending := make(chan rpcResponse, 1)
	c.pending[id] = pending
	c.mu.Unlock()
	message := map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}
	if err := c.write(message); err != nil {
		c.removePending(id)
		return err
	}
	select {
	case <-ctx.Done():
		c.removePending(id)
		return fmt.Errorf("MCP %s %s: %w", c.name, method, ctx.Err())
	case response := <-pending:
		if response.Error != nil {
			return fmt.Errorf("MCP error %d: %s", response.Error.Code, response.Error.Message)
		}
		if target == nil || len(response.Result) == 0 {
			return nil
		}
		if err := json.Unmarshal(response.Result, target); err != nil {
			return fmt.Errorf("decode MCP %s result: %w", method, err)
		}
		return nil
	}
}

func (c *connection) notify(method string, params any) error {
	return c.write(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

func (c *connection) write(message any) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.stdin == nil {
		return fmt.Errorf("MCP server %q is not connected", c.name)
	}
	encoded, err := json.Marshal(message)
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	_, err = c.stdin.Write(encoded)
	return err
}

func (c *connection) removePending(id int64) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

func (c *connection) failPending(err error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	pending := c.pending
	c.pending = map[int64]chan rpcResponse{}
	c.mu.Unlock()
	for id, channel := range pending {
		channel <- rpcResponse{ID: id, Error: &rpcError{Code: -32000, Message: err.Error()}}
		close(channel)
	}
}

func (c *connection) close() {
	c.mu.Lock()
	alreadyClosed := c.closed
	c.mu.Unlock()
	if !alreadyClosed {
		c.failPending(errors.New("MCP connection closed"))
	}
	if c.stdin != nil {
		_ = c.stdin.Close()
	}
	c.cancel()
	if c.cmd != nil && c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
	select {
	case <-c.done:
	case <-time.After(2 * time.Second):
	}
}

func cloneConfigs(source map[string]ServerConfig) map[string]ServerConfig {
	if source == nil {
		return nil
	}
	result := make(map[string]ServerConfig, len(source))
	for name, config := range source {
		config.Args = append([]string(nil), config.Args...)
		config.Env = cloneEnv(config.Env)
		result[name] = config
	}
	return result
}

func cloneEnv(source map[string]string) map[string]string {
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func cloneSchema(source map[string]any) map[string]any {
	if source == nil {
		return nil
	}
	encoded, _ := json.Marshal(source)
	var result map[string]any
	_ = json.Unmarshal(encoded, &result)
	return result
}

func mergeEnv(base []string, overrides map[string]string) []string {
	values := map[string]string{}
	for _, item := range base {
		if index := strings.IndexByte(item, '='); index >= 0 {
			values[item[:index]] = item[index+1:]
		}
	}
	for key, value := range overrides {
		values[key] = value
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+values[key])
	}
	return result
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
