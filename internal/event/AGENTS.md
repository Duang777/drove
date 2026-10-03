# AGENTS.md — internal/event

## 职责

**事件模型与扇出 Hub**。所有状态变化、输出增量、错误都以不可变 `Event` 表达；`Hub` 负责将事件广播给所有订阅者（daemon API、CLI、回放器）。

## 关键设计

- `Event` 不可变：字段导出但只读约定，构造一律走 `New*` 系列。
- `Type` 分类：`StateChanged` / `Output` / `Error` / `SessionLifecycle` / `AgentInput` / `AgentSignal`。
- `AgentInput` 只记录脱敏审计元数据，不记录用户输入正文。
- `Hub` 内部用注册表 + buffered channel 扇出；订阅者需在注册时声明 buffer 大小，Hub 不阻塞发布者（慢订阅者被丢弃并计数，见 `Dropped`）。
- `Hub` 只接受已分配非零序号的提交事件；全局序号由 `internal/session` 的 Committer 独占分配。
- 事件带 `Seq` 全局递增序号与 `Timestamp`，是回放（`internal/session`）的排序依据。

## 约束

- 禁止在本包引入厂商适配或持久化依赖（存储由 store 包消费 Event）。
- 事件必须能被序列化为 JSON（供 WebSocket/日志），新增字段需保持向后兼容（只增不删）。
- 导出类型：`Event`、`Type`、`Hub`、`Subscription`。
