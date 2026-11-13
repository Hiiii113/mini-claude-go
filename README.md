# Mini Claude Go

[![CI](https://github.com/Hiiii113/mini-claude-go/actions/workflows/ci.yml/badge.svg)](https://github.com/Hiiii113/mini-claude-go/actions/workflows/ci.yml)
[![Release](https://github.com/Hiiii113/mini-claude-go/actions/workflows/release.yml/badge.svg)](https://github.com/Hiiii113/mini-claude-go/actions/workflows/release.yml)
[![License](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

一个使用 Go 从头实现的命令行编码 Agent，支持 Anthropic 和 OpenAI-compatible 模型协议。

> 本项目全程使用 Codex 实现，仅用于 Agent 方向的学习和实验。

## 功能

- Windows、Linux、macOS 单二进制运行
- Anthropic 与 OpenAI-compatible 双后端
- 文件读写、精确编辑、正则搜索、Shell、网页读取
- 权限确认、Plan Mode、会话恢复和上下文压缩
- 项目记忆、Skills、子 Agent
- MCP JSON-RPC 2.0 stdio 工具扩展

## 使用 Release

打开 [Releases](https://github.com/Hiiii113/mini-claude-go/releases/latest)，下载并解压压缩包。

进入解压后的目录，把 `.env.example` 复制为 `.env`，然后填写自己的 API Key、模型名称和 Base URL。

Windows PowerShell：

```powershell
Copy-Item .env.example .env
notepad .env
.\mini-claude.exe
```

Linux / macOS：

```bash
cp .env.example .env
${EDITOR:-nano} .env
chmod +x mini-claude
./mini-claude
```

使用 OpenAI-compatible 服务时，`.env` 可以这样填写：

```dotenv
OPENAI_API_KEY=your-api-key
OPENAI_MODEL=gpt-4.1-mini
OPENAI_BASE_URL=https://api.openai.com/v1
```

使用 Anthropic 时：

```dotenv
ANTHROPIC_API_KEY=your-api-key
ANTHROPIC_MODEL=claude-sonnet-4-5
# ANTHROPIC_BASE_URL=https://api.anthropic.com
```

如果同时配置 OpenAI 和 Anthropic，程序优先使用 OpenAI。

执行一次任务并退出：

```powershell
.\mini-claude.exe "阅读当前项目并说明入口文件的作用"
```

```bash
./mini-claude "阅读当前项目并说明入口文件的作用"
```

## 使用源码

需要 Go 1.26.2 或更高版本：

```bash
git clone https://github.com/Hiiii113/mini-claude-go.git
cd mini-claude-go
```

在项目根目录把 `.env.example` 复制为 `.env`，并填写自己的模型配置。

Windows PowerShell：

```powershell
Copy-Item .env.example .env
notepad .env
go run main.go
```

Linux / macOS：

```bash
cp .env.example .env
${EDITOR:-nano} .env
go run main.go
```

执行一次任务：

```bash
go run main.go "检查当前项目并给出改进建议"
```

## 常用参数

```text
-m, --model <name>   指定模型
    --api-base <url> 指定 OpenAI-compatible API 地址
    --thinking       启用扩展思考
    --resume         恢复最近会话
    --plan           进入只读计划模式
    --accept-edits   自动允许文件编辑
    --dont-ask       自动拒绝需要确认的操作
-y, --yolo           跳过所有权限确认
    --max-cost <usd> 设置费用上限
    --max-turns <n>  设置最大模型轮数
```

示例：

```bash
go run main.go --accept-edits "为项目补充测试"
go run main.go --plan "规划一次目录重构"
go run main.go --resume
```

## REPL 命令

| 命令 | 作用 |
| --- | --- |
| `/clear` | 清空当前对话 |
| `/plan` | 切换 Plan Mode |
| `/cost` | 查看 token 和估算费用 |
| `/compact` | 压缩上下文 |
| `/memory` | 查看记忆 |
| `/skills` | 查看 Skills |
| `/<skill> [参数]` | 调用 Skill |
| `exit` / `quit` | 退出 |

模型执行过程中按一次 `Ctrl+C` 会取消当前任务；空闲时连续按两次 `Ctrl+C` 会退出。

## 项目扩展

- `CLAUDE.md`：项目指令，支持 `@include`。
- `.claude/rules/*.md`：项目规则。
- `.claude/skills/<name>/SKILL.md`：项目 Skills。
- `.claude/agents/*.md`：自定义子 Agent。
- `.mcp.json`：项目 MCP 服务配置。

MCP 示例：

```json
{
  "mcpServers": {
    "example": {
      "command": "your-mcp-server",
      "args": []
    }
  }
}
```

MCP 配置会启动本机进程，只使用可信服务。

## 开发与测试

```bash
gofmt -l .
go test ./...
go vet ./...
go test -race ./...
```

默认测试不会访问真实模型。完整验收清单见 [test/TEST-GUIDE.md](test/TEST-GUIDE.md)。

## 发布 Release

仓库中的 [release.yml](.github/workflows/release.yml) 会在推送 `v*` 标签后自动测试、打包并创建 GitHub Release。

提交代码后执行：

```bash
git push origin main
git tag -a v0.1.1 -m "Mini Claude Go v0.1.1"
git push origin v0.1.1
```

然后在仓库的 **Actions → release** 查看进度。发布下一个版本时使用新的标签，例如 `v0.1.2`，不要移动已经公开的标签。

## 安全

- 不要提交 `.env` 或真实 API Key。
- 谨慎使用 `--yolo` 和 `run_shell`。
- 运行陌生项目之前检查 `.mcp.json` 和 `.claude/settings.json`。
- 如果密钥曾出现在 Git 历史或公开文件中，请立即撤销并重新生成。

## 许可

本项目基于 MIT License 发布，详见 [LICENSE](LICENSE)。

## 署名

README 的审阅、校对与文字整理：Qgent。
