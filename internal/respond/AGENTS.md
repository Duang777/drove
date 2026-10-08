# AGENTS.md - internal/respond

## 职责

**远程响应编排层**：协调 `notify` 的一次性 ticket 与 `session` 的实时
ActionContext/Respond，不拥有终端、状态机或 HTTP 协议。

## 顺序约束

- `Context` 先向 session 确认同一 Blocked 序号仍可操作，再为指定活动设备签发
  action-bound tickets。
- `Execute` 先验证并持久消费 ticket，再调用 session 写 PTY。
- ticket 消费成功后不做补偿。后续 stale、busy、partial write 或 audit failure
  都保留已消费状态。
- Agent、Blocked 序号、action、channel 和 device 必须来自已验证 claims。

## 安全边界

- 本包不得记录或返回 ticket、reply、终端文本或厂商输入字节。
- 本包不得直接访问 SQLite、PTY、adapter 或 HTTP request。
- 错误只 wrap 并保留 `errors.Is` 语义，状态码映射由 `internal/api` 负责。
