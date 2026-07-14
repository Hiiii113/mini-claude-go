# Mini Claude Go 验收清单（19 项）

默认先运行 `go test ./...`。下列“自动化覆盖”是无需真实 API 的行为测试；“人工复核”用于发布前观察终端交互。涉及模型的人工步骤会产生费用。

## 1. MCP 工具发现与调用

- 自动化覆盖：`internal/mcp.TestConnectDiscoverRegisterAndCall`、`TestRequestTimeout`、`TestLoadConfigsUsesExpectedPrecedence`。
- 人工复核：在可信项目中配置 `.mcp.json`，运行程序并确认显示服务名、工具数，随后让模型调用一个 `mcp__server__tool`。

## 2. WebFetch 网页读取

- 自动化覆盖：`internal/tools.TestWebFetchConvertsHTMLToReadableText`。
- 人工复核：让 Agent 读取一个公开的 HTTPS 页面，确认脚本和样式未进入结果。

## 3. 安全只读工具并发

- 自动化覆盖：`internal/agent.TestConcurrencySafeToolsRunInParallelAndKeepOrder`、`TestWriteToolsRemainSerial`。
- 人工复核：要求同时读取多个文件，确认结果顺序与工具调用顺序一致。

## 4. 语义记忆召回

- 自动化覆盖：`internal/memory.TestSemanticSelectionAndFormatting`、`internal/agent.TestMemoryPrefetchInjectsRelevantMemory`。
- 人工复核：写入一条项目记忆，新会话询问相关主题，确认只注入相关记忆。

## 5. CLAUDE.md、include 与 rules

- 自动化覆盖：`internal/prompt.TestLoadProjectInstructionsAndIncludes`、`TestCircularIncludeIsReported`、`TestBuildIncludesInjectedSections`。
- 人工复核：创建嵌套 `CLAUDE.md`、`@include` 与 `.claude/rules/*.md`，询问 Agent 当前项目规则。

## 6. Read-before-edit 与 mtime 防护

- 自动化覆盖：`internal/tools.TestReadBeforeEditAndQuoteNormalization`。
- 人工复核：先直接编辑文件应被拒绝；读取后外部修改文件，再编辑仍应被拒绝。

## 7. 大工具结果完整落盘

- 自动化覆盖：`internal/agent.TestLargeToolResultPersistsFullOutput`、`internal/contextmgr.TestPrepareBudgetsAndSnipsOldResults`。
- 人工复核：读取超大文件，确认终端给出摘要，`~/.mini-claude/tool-results` 中保存完整内容。

## 8. Skill inline 与 fork

- 自动化覆盖：`internal/agent.TestSkillInlineAndForkExecution`、`internal/skills.TestDiscoverProjectOverridesAndResolve`。
- 人工复核：分别运行一个 inline Skill 和 `context: fork` Skill，确认后者历史隔离。

## 9. 延迟 ToolSearch

- 自动化覆盖：`internal/tools.TestToolSearchActivatesDeferredTools`。
- 人工复核：让模型搜索 plan 工具，确认工具搜索后才激活延迟定义。

## 10. REPL 命令

- 自动化覆盖：`internal/cli.TestRunDispatchesCommandsAndChat`、`TestConfirmationUsesSameInputStream`、`TestPlanApprovalReadsFeedback`。
- 人工复核：依次运行 `/cost`、`/memory`、`/skills`、`/compact`、`/clear` 和 `/plan`。

## 11. 内置子 Agent

- 自动化覆盖：`internal/subagent.TestBuiltInAgentsHaveExpectedTools`、`internal/agent.TestSubAgentUsesIsolatedHistoryAndAddsUsage`。
- 人工复核：分别委派 explore、plan、general 任务，观察状态和工具限制。

## 12. Plan Mode 审批

- 自动化覆盖：`internal/agent.TestPlanModeApprovalChoices`、`TestPlanModeOnlyAllowsPlanFileWrite`、`TestPlanClearApprovalResetsToValidHistory`。
- 人工复核：覆盖清空后执行、保留后执行、手动执行、反馈后继续规划四种选择。

## 13. 弯引号规范化和编辑预览

- 自动化覆盖：`internal/tools.TestReadBeforeEditAndQuoteNormalization`。
- 人工复核：用弯引号指定旧文本，确认唯一匹配并显示 `-`/`+` 编辑预览。

## 14. 会话恢复与旧格式迁移

- 自动化覆盖：`internal/session.TestSaveLoadAndLatest`、`TestLoadLegacyOpenAI`、`TestLoadLegacyAnthropic`、`internal/agent.TestAgentAutoSavesNeutralSession`。
- 人工复核：完成一次会话后用 `--resume` 启动，确认历史与费用恢复。

## 15. One-shot 模式

- 自动化覆盖：`internal/cli.TestOneShotPromptRunsExactlyOnce`、`TestRootCommandPassesCompatibleFlags`。
- 人工复核：在任意项目目录执行 `mini-claude "只回答 OK"`，确认回答后程序退出。

## 16. 最大轮次限制

- 自动化覆盖：`internal/agent.TestMaxTurnsStopsBeforeAnotherModelCall`。
- 人工复核：使用 `--max-turns 1` 发起需要工具的任务，确认第一次模型轮次后停止。

## 17. Grep 正则与 include

- 自动化覆盖：`internal/tools.TestGrepSearchSupportsRegexAndRecursiveInclude`。
- 人工复核：搜索正则并指定 `**/*.go`，确认只返回匹配扩展名及行号。

## 18. 新建嵌套文件与预览

- 自动化覆盖：`internal/tools.TestWriteFileCreatesNestedDirectoryAndLimitsPreview`。
- 人工复核：写入超过 30 行的嵌套新文件，确认目录自动创建、文件完整、终端预览不超过 30 行。

## 19. 自定义 Agent

- 自动化覆盖：`internal/subagent.TestProjectCustomAgentOverridesUser`、`internal/agent.TestParentAbortCancelsSubAgent`、`TestGeneralSubAgentDoesNotBypassParentPermission`。
- 人工复核：在 `.claude/agents/*.md` 定义项目 Agent，确认覆盖同名用户 Agent，且父级取消和权限继续生效。

## 发布前附加验证

```powershell
gofmt -l .
go test ./...
go vet ./...
go test -race ./...
go install .
```

真实后端冒烟测试默认关闭。需要时分别配置模型名和密钥，然后执行：

```powershell
$env:RUN_LIVE_LLM_TESTS = "1"
go test ./internal/llm -run Live -v
```
