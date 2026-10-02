# AGENTS.md — internal/agent

## 职责

Agent 的**抽象与状态机**。这是全项目唯一的状态权威（source of truth）：任何组件要判断一个 agent 处于什么状态，都必须通过本包的 `Agent.State()`，不得自行猜测。

## 关键设计

- `State` 枚举：`Working` / `Blocked` / `Done` / `Idle`，外加 `Starting` / `Stopped` 作为生命周期边界态。
- `Agent.Transition(to State, reason string)`：唯一允许改状态的入口；非法迁移返回错误（见 `state.go` 中 `transitions` 表）。
- Agent 本身**不产生事件**，只暴露状态变更的观察钩子 `OnStateChange`；事件由上层（session/event 包）负责落库与扇出。

## 约束

- 禁止在此包 import 任何厂商适配或 PTY 实现。
- 状态迁移规则修改必须同步更新 `transitions` 表与测试 `state_test.go`。
- 导出类型：`Agent`、`State`、`ID`。
