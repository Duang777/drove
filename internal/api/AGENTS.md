# AGENTS.md — internal/api

## 职责

**对外 API 面**：REST 管理接口 + WebSocket 事件流。CLI 与 Web 前端都通过这里与
daemon 通信。

## 关键设计

- `Server` 封装 `http.Server`，路由：
  - `GET  /api/v1/agents`：列出会话
  - `POST /api/v1/agents`：启动会话（JSON body → StartRequest）
  - `GET  /api/v1/agents/{id}`：单会话状态
  - `DELETE /api/v1/agents/{id}`：停止会话
  - `POST /api/v1/agents/{id}/resume`：在同一 Agent ID 下执行厂商原生恢复
  - `POST /api/v1/agents/{id}/input`：向已连接 PTY 写入一段受限 UTF-8 文本
  - `POST /api/v1/agents/{id}/signal`：接收 loopback vendor hook relay
  - `GET  /api/v1/agents/{id}/explain`：返回受限决策尾部与可选 attached screen
  - `GET  /api/v1/agents/{id}/events`：回放事件流（REST，JSON 数组）
  - `POST /api/v1/auth/login-code`：仅允许 Unix socket 签发一次性浏览器登录码
  - `POST /api/v1/auth/login`：浏览器同源兑换 HttpOnly cookie
  - `POST /api/v1/auth/token/rotate`：仅允许 Unix socket 调用的控制令牌轮换
  - `GET  /api/v1/agents/{id}/timeline`：状态区间、Blocked 索引与输出保留范围
  - `GET  /api/v1/agents/{id}/timeline/blocked/{number}`：一基 Blocked 跳转位置
  - `GET  /api/v1/agents/{id}/frame`：按 seq、at 或 offset 精确重建受限终端帧
  - `GET  /ws`：WebSocket 实时事件流与版本化双向输入
- 浏览器 listener 从嵌入文件系统提供 `/`、`/login` 和哈希静态资源；Unix listener
  不提供前端。
- 每个 listener 先绑定访问类型和精确 Host 白名单；所有路由（含 signal 与静态资源）都
  在认证前校验 Host，浏览器边界对任何已携带的 Origin 做精确白名单校验。
- 控制 REST 与 WebSocket 接受 Bearer 或浏览器 cookie；cookie 认证的非安全请求和
  WebSocket 必须携带 Origin，Bearer 请求可以省略 Origin。
- signal endpoint 是唯一例外：它只接受 loopback TCP 或已由 listener 校验的本机
  Unix peer，并使用目标会话的内存 token，不接受控制面 token；请求 envelope
  必须严格校验，只有已提交或重复的 delivery 返回 204。
- 每个 WebSocket 连接只有一个读协程和一个写协程；写协程独占事件、ack/error、ping/pong 和 close 帧。
- v1 和 v2 WebSocket 都监听认证 `Grant.Done()`；令牌代际或 cookie session
  失效后发送 policy violation close。
- 无子协议继续使用 v1；只有精确协商 `drove.v2` 才启用按 Agent 的 raw、events 和
  snapshot 订阅。其它显式子协议在 upgrade 前拒绝。
- v2 的普通出站帧共享 8 MiB 字节预算；溢出会取消该连接的全部订阅、丢弃未写帧，
  通过保留控制槽发送各订阅最后成功写出的 cursor，并以 1013 关闭。
- resume cursor 只在 writer 成功写完整帧后推进；history 和 live 必须使用同一
  Store tail，订阅建立竞态不得通过 Hub 补洞。
- v2 raw subscribe 保留 `writable` 字段 presence：省略是无审计 recording
  reader，显式 `false` 是用户只读 attachment，显式 `true` 是用户可写
  attachment；selector 不表达 attachment intent。snapshot 使用无审计 recording
  attachment。
- user attachment 建立和清理分别提交一条 `agent.attachment`。payload 只含
  version、action 和 access，不含 attachment ID 或客户端身份。
- Agent 状态响应中的可选 `dir` 来自 session 的 creation metadata。旧事件没有该
  字段时省略；API 不推断或补写工作目录。
- v1 hello、事件和输入错误的逐帧字节形状由
  `testdata/websocket_v1.golden` 锁定。
- REST 回放和 WebSocket 都原样传输已提交的 `output.chunk` 事件；保留期内 payload
  含 Base64 正文，过期回放只含 offset/len 元数据。
- frame 只接受一个 selector；缺失录制返回 404，selector 错误返回 400，需要的
  output 已过期时返回带 `output_expired` code 和 missing ranges 的 410。
- 输入消息必须携带版本、连接内唯一 `request_id` 和 Agent ID；响应以同一 `request_id` 返回稳定 ack/error。
- 输入背压统一映射为 REST 503；WebSocket v1/v2 使用稳定错误码
  `input_backpressure`。部分送达错误保留 `do not retry` 提示。
- 处理函数保持薄：解析→调用 Manager→序列化；业务逻辑不得进入本包。
- 统一 JSON 错误格式：`{"error": "..."}`，HTTP 状态码语义化。
- WebSocket 发送带 write deadline + ping/pong 保活，防止死连接。

## 约束

- 禁止在本包 import pty / store / adapter 实现细节；只依赖 session.Manager 与 event.Hub 的公开接口。
- 新端点必须加路由注册与（如有）测试。
- 导出类型：`Server`、`ServerOptions`。
