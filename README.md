# Mini Claude Go

`mini-claude` 是 [claude-code-from-scratch](../cc%20from%20scratch) 的 Go 重写版，目标是在单个跨平台二进制中实现 Agent Loop、工具、权限、会话、记忆、Skills、子 Agent 与 MCP。

## 开发

```powershell
Copy-Item .env.example .env
go test ./...
go run . --help
go install .
```

支持 Anthropic 原生协议和 OpenAI-compatible 协议。API Key 只能通过环境变量或未提交的 `.env` 文件提供。

## 安全

- 不要提交 `.env`、会话、记忆或工具结果。
- `--yolo` 会跳过确认，仅应在隔离环境中使用。
- `run_shell` 能执行本机命令，默认权限模式会对危险命令请求确认。

## 来源与许可

本项目基于 Windy3f3f3f3f 的 MIT 许可项目 `claude-code-from-scratch` 重写，保留原始许可证与版权声明。

