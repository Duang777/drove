# AGENTS.md - internal/notify

## 职责

**通知投递域**：从已发布的持久事件前缀派生通知状态，维护订阅、游标、
outbox、投递租约与短期浏览器 presence，并通过渠道接口投递。

## 关键设计

- `drove.db` 是唯一事件来源。Hub 只唤醒 planner，不能作为通知队列。
- planner 的扫描上限不得超过 `Hub.LastSeq()`。
- `notify.db` 独立保存可变状态。游标推进与 outbox 创建必须在同一事务内。
- 首次启用把游标初始化到当前已发布序号，不补发历史通知。
- 通知 payload 由固定字段构造，不得复制事件 payload、终端内容或输入正文。
- 投递采用有界租约和至少一次语义。过期租约可以在重启后重新领取。
- Web Push 与 ntfy 只实现 `Channel`，不得反向依赖 daemon、API 或 session。
- Web Push 测试通知直接发送到一个指定订阅，不写 session 事件或改变 Agent 状态。
- Web Push 每次 provider 尝试前才查询实时可用 action 并签发新 ticket；outbox
  payload 不持久化 ticket。
- action ticket 使用独立 256-bit HMAC key。数据库只保存 JTI 的 SHA-256 摘要和
  有界绑定信息，消费事务必须先于 session 响应。
- 撤销 Web Push 设备时必须同时失效该设备尚未消费的 action ticket。
- API 可读取公开设备 ID、名称和创建时间，但不得读取 endpoint 或订阅密钥。

## 约束

- 本包不得修改 Agent 状态或向 PTY 写入数据。
- 不得记录订阅密钥、endpoint、VAPID 私钥、action ticket、ntfy token 或完整通知
  payload。
- 所有数据库文件必须是普通 `0600` 文件，迁移使用递增的
  `PRAGMA user_version`。
- 所有 SQL 使用参数化查询。库代码返回带上下文的 error。
