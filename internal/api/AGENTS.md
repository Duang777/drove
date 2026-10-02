# AGENTS.md — internal/api

## 职责

**对外 API 面**：REST 管理接口 + WebSocket 事件流。CLI 与（未来的）Web 前端都通过这里与 daemon 通信。

## 关键设计

- `Server` 封装 `http.Server`，路由：
  - `GET  /api/v1/agents`：列出会话
  - `POST /api/v1/agents`：启动会话（JSON body → StartRequest）
  - `GET  /api/v1/agents/{id}`：单会话状态
  - `DELETE /api/v1/agents/{id}`：停止会话
  - `GET  /api/v1/agents/{id}/events`：回放事件流（REST，JSON 数组）
  - `GET  /ws`：WebSocket 实时事件流（每订阅一个连接）
- 处理函数保持薄：解析→调用 Manager→序列化；业务逻辑不得进入本包。
- 统一 JSON 错误格式：`{"error": "..."}`，HTTP 状态码语义化。
- WebSocket 发送带 write deadline + ping/pong 保活，防止死连接。

## 约束

- 禁止在本包 import pty / store / adapter 实现细节；只依赖 session.Manager 与 event.Hub 的公开接口。
- 新端点必须加路由注册与（如有）测试。
- 导出类型：`Server`、`ServerOptions`。
