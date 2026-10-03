# AGENTS.md - internal/detect

## 职责

无副作用的状态信号决策包。Detector 根据 Agent 快照、Detector 快照和一个
Observation 计算不可变 Decision；goroutine、队列和真实计时器由 session 持有。

## 关键设计

- hook 只有在当前会话持久化首个合法信号并应用 Decision 后才成为权威。
- `auto` 在 hook 激活前使用启发式，`off` 只使用启发式，`required` 只使用 hook。
- Codex legacy notify 是非权威来源：不激活 hook、不满足 `required`，只在
  `fallback` 中生成可取消的 Idle 候选；`awaiting_hook` 与 `hook_active` 会抑制它。
- Detector State 持有 delivery ID 去重、候选、timer generation、Blocked 输出恢复
  和 hook 状态；只有已提交 Decision 可以修改它。
- Detector 不修改 Agent、不运行 goroutine，也不写 Store。session actor 负责串行
  提交 signal、可选 error 和可选 state_changed。
- 进程启动、失败和退出也是 Observation，并拥有高于 hook 和启发式的优先级。

## 约束

- 禁止解析 Claude 或 Codex JSON；厂商字段只允许存在于 `internal/adapter`。
- 禁止依赖 adapter、HTTP、PTY、Store 或 event Hub。
- Detector 只返回 deadline 和 generation，不创建真实计时器。
- 导出类型以 `Detector`、`State`、`Snapshot`、`Observation`、`Decision`、
  `Signal`、`Config` 和 `Clock` 为核心。
