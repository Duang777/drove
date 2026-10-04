# AGENTS.md — internal/recording

## 职责

**终端录制只读领域层**：基于不可变事件与输出附件解析游标、读取历史与实时尾部、
投影状态时间线，并从原点精确重建终端帧。

## 关键设计

- SQLite 事件和 `output_chunks` 附件是唯一事实源；通知、缓存和 live snapshot
  都只能作为可丢弃的加速或唤醒机制。
- canonical cursor 由最后消费的会话事件序号和下一个未消费输出字节偏移组成。
- sequence 是全局序号，在单个会话内允许稀疏；output offset 是会话局部且使用
  exclusive next-offset 语义。
- range/tail 读取必须有行数和附件字节上限，不能跨网络等待持有数据库游标。
- timeline 只依赖事件 envelope；输出附件过期后仍可读取。
- frame 始终从 40x120 原点精确回放；可见 snapshot 不得充当可恢复 checkpoint。

## 约束

- 本包可依赖 `agent`、`event`、`store`、`term`，不得依赖 `session`、`pty`、
  `adapter`、`detect`、`api` 或事件 Hub。
- 不持久化渲染屏幕、客户端 attachment ID、输入正文或终端查询应答。
- 对外只暴露领域对象和类型化错误，不暴露 SQL 行或传输协议 DTO。
