# AGENTS.md — internal/agent

## 职责

Agent 的**抽象与状态机**。这是全项目唯一的状态权威（source of truth）：任何组件要判断一个 agent 处于什么状态，都必须通过本包的 `Agent.State()`，不得自行猜测。

## 关键设计

- `State` 枚举：`Working` / `Blocked` / `Done` / `Idle`，外加 `Starting` / `Stopped` 作为生命周期边界态。
- `Blocked -> Idle` 表示 hook 确认当前 turn 已结束但交互进程仍存活。
- `RunMode` 是不可变运行元数据：新 Agent 默认 `interactive`，历史恢复必须提供已校验的 `interactive` 或 `oneshot`。
- `PlanTransition` 在持久化前校验并生成带 revision 的计划，`ApplyTransition` 只在事件提交后应用；陈旧计划不得覆盖新状态。
- `Agent.Transition` 是包独立使用时的同步便利入口；会话编排必须使用 plan/apply 两阶段接口。
- Agent 本身**不产生事件**；事件由上层（session/event 包）负责落库与扇出。

## 约束

- 禁止在此包 import 任何厂商适配或 PTY 实现。
- 状态迁移规则修改必须同步更新 `transitions` 表与测试 `state_test.go`。
- 导出类型：`Agent`、`State`、`RunMode`、`ID`。
