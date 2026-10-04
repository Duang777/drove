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
- raw tail 只发持久化 `output.chunk` 字节和 `agent.resized`；event tail 发完整
  public envelope，并统一删除创建目录与 vendor session ref。两者固定初始 durable
  head，发出 caught-up 后再跟随 commit clock。
- 缺失输出附件必须先返回类型化 `OutputExpiredError` 并终止该 tail，禁止跳过缺口
  继续发送后续字节。
- timeline 只依赖事件 envelope；输出附件过期后仍可读取。
- timeline 的状态区间为半开区间，Blocked 使用一基序号并返回进入前 30 秒的跳转
  cursor；输出范围同时报告 retained 区间和合并后的 missing 区间。
- frame 始终从 40x120 原点精确回放；可见 snapshot 不得充当可恢复 checkpoint。
  只缓存完成的精确响应，key 包含目标 cursor、view bounds 和 Store retention
  generation；回放前后 generation 变化时丢弃结果并重试。
- frame 只返回目标时刻的受限可见 cells，不暴露 x/vt 内部状态。客户端只能把它
  用作预览，不能从 frame 继续精确回放。
- 50 MiB 冷 frame 的性能基线由 `BenchmarkFrame50MiBColdRandom` 记录。未引入
  可恢复的完整 x/vt checkpoint 前，不得用可见 snapshot 换取速度；后续见 #35。

## 约束

- 本包可依赖 `agent`、`event`、`store`、`term`，不得依赖 `session`、`pty`、
  `adapter`、`detect`、`api` 或事件 Hub。
- 不持久化渲染屏幕、客户端 attachment ID、输入正文或终端查询应答。
- 对外只暴露领域对象和类型化错误，不暴露 SQL 行或传输协议 DTO。
