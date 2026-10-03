# AGENTS.md - internal/detect

## 职责

每个运行中会话的状态信号融合器。Detector 用一个 goroutine 串行处理 hook、
终端启发式和计时器信号，并输出零个或一个状态迁移建议。

## 关键设计

- hook 只有在当前会话收到并持久化首个合法信号后才成为权威。
- `auto` 在 hook 激活前使用启发式，`off` 只使用启发式，`required` 只使用 hook。
- Detector 独占 delivery ID 去重、置信度阈值、Blocked 输出恢复和 Idle 确认窗口。
- Detector 不修改 Agent，也不写 Store。调用方负责把脱敏 signal 和状态事件作为
  一个提交批次持久化。
- 进程退出由 session 生命周期处理，并在提交终态前关闭 Detector。

## 约束

- 禁止解析 Claude 或 Codex JSON；厂商字段只允许存在于 `internal/adapter`。
- 禁止依赖 HTTP、PTY、Store 或 event Hub。
- 所有计时器必须可注入，以便竞态测试不依赖真实等待。
- 导出类型：`Detector`、`Options`、`Policy`、`Decision`。
