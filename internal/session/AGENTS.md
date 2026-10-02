# AGENTS.md — internal/session

## 职责

**会话编排层**：把 agent 状态机、PTY、适配器、事件 Hub、存储串成一条线。上层（daemon/api/CLI）只与本包交互，不直接触碰 pty / adapter / store。

## 关键设计

- `Manager` 持有：`agents`（ID→*agent.Agent）、`sessions`（ID→*Session）、event Hub、store、adapter Registry。
- `Start(ctx, req)`：按 vendor 取适配器 → 构造 agent → 创建 PTY → 启动命令 → 注册状态变更钩子（把迁移落库 + 发布事件）→ 输出按行切分后发布 Output 事件 → 进程退出时迁移 Stopped。
- `Replay(sessionID)`：从 store 读取事件流供回放（CLI `log` 命令 / API）。
- 状态决策：优先采纳适配器 hint；结合"进程是否存活"（存活→Working，退出→Stopped/Done）兜底，防止误判。

## 约束

- 禁止在 session 之外创建 agent 或 PTY 会话。
- 事件必须**先落库后发布**（保证回放与实时一致），见 `persistAndPublish`。
- 会话关闭必须幂等（多次 Close 不 panic、不泄漏 goroutine）。
- 导出类型：`Manager`、`Session`、`StartRequest`、`Status`。
