# AGENTS.md — internal/api

## 职责

**对外 API 面**：REST 管理接口 + WebSocket 事件流。CLI 与（未来的）Web 前端都通过这里与 daemon 通信。

## 关键设计

- `Server` 封装 `http.Server`，路由：
  - `GET  /api/v1/agents`：列出会话
  - `POST /api/v1/agents`：启动会话（JSON body → StartRequest）
  - `GET  /api/v1/agents/{id}`：单会话状态
  - `DELETE /api/v1/agents/{id}`：停止会话
  - `POST /api/v1/agents/{id}/input`：向已连接 PTY 写入一段受限 UTF-8 文本
  - `POST /api/v1/agents/{id}/signal`：接收 loopback vendor hook relay
  - `GET  /api/v1/agents/{id}/explain`：返回受限决策尾部与可选 attached screen
  - `GET  /api/v1/agents/{id}/events`：回放事件流（REST，JSON 数组）
  - `GET  /ws`：WebSocket 实时事件流与版本化双向输入
- 所有 REST 与 WebSocket 请求必须先通过本地 Bearer 令牌认证；WebSocket 还要求 Origin 缺失或与配置的本地 Origin 精确匹配。
- signal endpoint 是唯一例外：它只接受 loopback，并使用目标会话的内存 token，
  不接受控制面 token；请求 envelope 必须严格校验，只有已提交或重复的 delivery
  返回 204。
- 每个 WebSocket 连接只有一个读协程和一个写协程；写协程独占事件、ack/error、ping/pong 和 close 帧。
- REST 回放和 WebSocket 都原样传输已提交的 `output.chunk` 事件；保留期内 payload
  含 Base64 正文，过期回放只含 offset/len 元数据。
- 输入消息必须携带版本、连接内唯一 `request_id` 和 Agent ID；响应以同一 `request_id` 返回稳定 ack/error。
- 处理函数保持薄：解析→调用 Manager→序列化；业务逻辑不得进入本包。
- 统一 JSON 错误格式：`{"error": "..."}`，HTTP 状态码语义化。
- WebSocket 发送带 write deadline + ping/pong 保活，防止死连接。

## 约束

- 禁止在本包 import pty / store / adapter 实现细节；只依赖 session.Manager 与 event.Hub 的公开接口。
- 新端点必须加路由注册与（如有）测试。
- 导出类型：`Server`、`ServerOptions`。
