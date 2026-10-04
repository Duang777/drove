# AGENTS.md — internal/store

## 职责

**持久化层**：把事件流追加写入 SQLite（纯 Go 驱动 `modernc.org/sqlite`），并支持按会话/agent 查询回放。

## 关键设计

- **只追加**：事件日志只 INSERT，不 UPDATE/DELETE；重建会话视图 = 重放事件（事件溯源）。
- 表结构：`events(seq, ts, type, session_id, agent_id, from, to, reason, payload)`
  以 `seq` 为单调主键；`output_chunks(event_seq, data)` 保存可过期的原始输出附件。
- `Store` 封装独占写连接与有界读连接池，提供追加、兼容回放和短生命周期范围读取。
- WAL 模式开启（`_pragma=journal_mode(WAL)`），daemon 长生命周期下并发读写安全。
- 缺失的数据库文件在 SQLite 打开前以 `0600` 创建；既有数据库必须是普通文件，
  且本包不自动修改它的权限。
- Store 启用并验证 `foreign_keys` 与 `secure_delete`。
- `output.chunk` envelope 与附件在同一事务中追加；`ScanEvents` 不加载附件，
  `Replay` 通过 left join 返回仍保留的附件。
- `RecentEvents` 在 SQLite 内按会话和事件类型过滤、倒序截取有限尾部，再按
  `seq` 升序返回；它只读 event envelope，绝不联接或加载输出附件。
- `SessionBoundary` 在一个短读事务中捕获全局 durable head、会话序号边界和
  exclusive next output offset。
- `ReadSessionRange` 同时限制行数与 hydrated attachment 字节数；结果显式标记
  每个 output attachment 是否仍存在，返回前关闭查询游标。
- sequence、timestamp 和 output-offset 解析都只读取被 captured sequence
  限定的不可变前缀。
- `PruneOutputAttachments` 只删除截止时间前的 `output.chunk` 附件，保留所有 event
  envelope，并在每次清理后执行 `wal_checkpoint(TRUNCATE)`。

## 约束

- 禁止在本包引入事件模型以外的领域逻辑；状态机/适配器不得 import 本包。
- 所有 SQL 使用参数化查询，禁止字符串拼接（防注入）。
- 迁移用 `PRAGMA user_version` 递增；新表/列必须附带迁移函数。
- 导出类型：`Store`、`EventRow`。
