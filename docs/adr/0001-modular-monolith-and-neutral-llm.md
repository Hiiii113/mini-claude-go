# ADR-0001：模块化单体与中立 LLM 协议

## 状态

Accepted

## 背景

原 TypeScript 版本分别维护 OpenAI 与 Anthropic 消息和循环。Go 版本需要同时兼容两个后端，又要避免 Agent、工具和会话层依赖厂商 SDK 类型。

## 决策

应用保持单进程模块化单体。`internal/llm` 定义中立消息、内容块、工具调用和流事件；厂商 Adapter 只负责协议转换。Agent 只依赖 `llm.Client`、工具注册表和小型存储接口。

## 结果

- 两个后端共享同一个 Agent Loop、权限和上下文管理。
- 会话可以用中立格式持久化，并可增加旧格式迁移。
- Adapter 需要维护双向转换测试，但厂商变化不会扩散到业务包。

