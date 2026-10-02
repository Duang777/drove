# AGENTS.md — internal/session

## 职责

**会话编排层**：把 agent 状态机、PTY、适配器、事件 Hub、存储串成一条线。上层（daemon/api/CLI）只与本包交互，不直接触碰 pty / adapter / store。

## 关键设计

- `Manager` 持有：`agents`（ID→*agent.Agent）、`sessions`（ID→运行中 PTY）、event Hub、store、adapter Registry。
- `Start(ctx, req)`：校验并默认 `RunMode` → 按 vendor 取适配器 → 构造 agent → 持久化 `starting` → 创建带固定回调的 PTY → 登记会话并持久化 `working` → 放行输出和退出回调。
- 新请求默认 `interactive`；旧事件缺少 mode 时由恢复投影回退为 `oneshot`。
- PTY 回调在启动前注册，但通过单次 ready channel 等待会话登记完成，防止短进程的输出或退出越过 `starting -> working`。
- 运行中会话记录停止原因和退出认领状态；`Stop`、`Close` 与自然退出通过同一个锁确定唯一终态。
- oneshot 自然成功退出为 `done`；interactive、失败退出和已登记的主动停止为 `stopped`。
- `Close()`：拒绝新 Start → 等待进行中的 Start → 关闭全部 PTY 并等待回调 → 清空运行中会话索引。
- `Replay(sessionID)`：从 store 读取事件流供回放（CLI `log` 命令 / API）。
- 状态决策：优先采纳适配器 hint；结合"进程是否存活"（存活→Working，退出→Stopped/Done）兜底，防止误判。

## 约束

- 禁止在 session 之外创建 agent 或 PTY 会话。
- 事件必须**先落库后发布**（保证回放与实时一致），见 `persistAndPublish`。
- 会话关闭必须幂等（多次 Close 不 panic、不泄漏 goroutine）。
- 导出类型：`Manager`、`Session`、`StartRequest`、`Status`。
