# AGENTS.md — internal/event

## 职责

**事件模型与扇出 Hub**。所有状态变化、输出增量、错误都以不可变 `Event` 表达；`Hub` 负责将事件广播给所有订阅者（daemon API、CLI、回放器）。

## 关键设计

- `Draft` 不含序号和时间；只有 Committer 能通过 `Commit` 把它封成只读 `Event`。
- `Type` 分类：`StateChanged` / `Output` / `OutputChunk` / `Error` /
  `SessionLifecycle` / `AgentInput` / `AgentSignal` / `AgentResized`。
- `output.chunk` 的公开 payload 使用版本化 Base64；持久化 metadata 与原始附件保存在
  `Event` 的私有字段中，访问器始终返回字节副本。
- `AgentInput` 只记录脱敏审计元数据，不记录用户输入正文。
- `Hub` 内部用注册表 + buffered channel 扇出；订阅者需在注册时声明 buffer 大小，Hub 不阻塞发布者（慢订阅者被丢弃并计数，见 `Dropped`）。
- `Hub.PublishBatch` 先校验整个连续批次，再向订阅者发布；全局序号由 `internal/session` 的 Committer 独占分配。
- `agent.signal` 与新 `state_changed` payload 使用版本化、可校验的脱敏元数据；
  v1 覆盖 Phase 1A 来源，v2 增加非权威 notify 来源，恢复层跳过未知审计版本。
- `agent.resumed` 记录显式原生恢复；SQLite 载荷保留 opaque vendor ref 供恢复校验，
  Hub 与其它公开事件视图只暴露版本号。
- `session_lifecycle(created)` 可分别携带公开 payload 与私有持久 payload；当前私有
  字段是原始绝对工作目录，Hub 只能发布公开版本。
- v3 增加可选的 typed screen attribution；生产 writer 只对 screen 来源写 v3，
  其他来源继续写原版本。screen attribution 只保存稳定静态元数据和已提交输出位置。
- v4 增加可选的 typed terminal attribution；terminal notify 不使用 delivery ID，
  只保存协议、已提交 output offset 和最终输出序号。
- `agent.resized` v1 记录成功生效的行列和当时的 exclusive output offset；
  attachment 身份不进入事件。
- 事件带 `Seq` 全局递增序号与 `Timestamp`，是回放（`internal/session`）的排序依据。

## 约束

- 禁止在本包引入厂商适配或持久化依赖（存储由 store 包消费 Event）。
- 事件必须能被序列化为 JSON（供 WebSocket/日志），新增字段需保持向后兼容（只增不删）。
- 导出类型：`Event`、`Type`、`Hub`、`Subscription`。
