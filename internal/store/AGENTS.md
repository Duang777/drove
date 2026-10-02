# AGENTS.md — internal/store

## 职责

**持久化层**：把事件流追加写入 SQLite（纯 Go 驱动 `modernc.org/sqlite`），并支持按会话/agent 查询回放。

## 关键设计

- **只追加**：事件日志只 INSERT，不 UPDATE/DELETE；重建会话视图 = 重放事件（事件溯源）。
- 表结构：`events(seq, ts, type, session_id, agent_id, from, to, reason, payload)`，以 `seq` 为单调主键。
- `Store` 封装 `database/sql`，提供 `AppendEvent` / `Replay(sessionID)` / `Close`。
- WAL 模式开启（`_pragma=journal_mode(WAL)`），daemon 长生命周期下并发读写安全。

## 约束

- 禁止在本包引入事件模型以外的领域逻辑；状态机/适配器不得 import 本包。
- 所有 SQL 使用参数化查询，禁止字符串拼接（防注入）。
- 迁移用 `PRAGMA user_version` 递增；新表/列必须附带迁移函数。
- 导出类型：`Store`、`EventRow`。
