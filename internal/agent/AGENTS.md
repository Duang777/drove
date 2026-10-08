# AGENTS.md — internal/agent

## 职责

Agent 的**抽象与状态机**。这是全项目唯一的状态权威（source of truth）：任何组件要判断一个 agent 处于什么状态，都必须通过本包的 `Agent.State()`，不得自行猜测。

## 关键设计

- `State` 枚举：`Working` / `Blocked` / `Done` / `Idle`，外加 `Starting` / `Stopped` 作为生命周期边界态。
- `Blocked -> Idle` 表示 hook 确认当前 turn 已结束但交互进程仍存活。
- `RunMode` 是不可变运行元数据：新 Agent 默认 `interactive`，历史恢复必须提供已校验的 `interactive` 或 `oneshot`。
- `HookPolicy` 是会话级不可变元数据；版本 1 历史记录恢复为 `off`。
- `SignalInjectionMode` 与注入结果是不可变启动元数据；它只描述进程参数和临时
  配置，不代表运行时 hook 已激活。
- `ActionKind` 只定义跨厂商的显式审批动作 `approve` / `deny` / `reply`，不包含
  自动批准。
- `Evidence` 是状态迁移的脱敏来源，恢复投影保留最近一条已理解的证据；
  screen 来源携带经过校验和复制的稳定规则、edge、region、输出偏移、最终输出序号
  与静态 evidence，不携带屏幕文本。
- terminal notify 来源携带 `osc9` 协议、已提交 output offset 和最终输出序号；
  不携带终端通知正文，也不使用 delivery ID。
- `Prepare` 在持久化前校验 typed change 并绑定 revision，`ApplyCommitted` 只在事件提交后应用；陈旧或外部 Agent 的 change 不得覆盖当前状态。
- `ResumeToStarting` 是唯一允许 `Stopped -> Starting` 的 typed change；普通迁移表继续把
  `Stopped` 视为终态。
- `Agent.Transition` 是包独立使用时的同步便利入口；会话编排必须使用 plan/apply 两阶段接口。
- Agent 本身**不产生事件**；事件由上层（session/event 包）负责落库与扇出。

## 约束

- 禁止在此包 import 任何厂商适配或 PTY 实现。
- 状态迁移规则修改必须同步更新 `transitions` 表与测试 `state_test.go`。
- 导出类型：`Agent`、`State`、`RunMode`、`HookPolicy`、
  `SignalInjectionMode`、`SignalInjectionStatus`、`SignalInjectionReason`、
  `Evidence`、`ScreenAttribution`、`ScreenEdge`、`TerminalAttribution`、
  `ActionKind`、`ID`。
