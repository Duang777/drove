# AGENTS.md — internal/client

## 职责

**daemon 客户端**：CLI 与 daemon 通信的 HTTP 封装，并负责在 daemon 未运行时自动拉起。

## 关键设计

- `Client` 封装 REST 调用（List / Start / Status / Stop / SendInput / Replay），基址来自 Config.APIBind。
- 每次请求从配置的数据目录读取控制令牌并发送 Bearer 认证，避免令牌轮换后持有过期值。
- `RelaySignal` 是独立的 hook 回调路径，只接受 loopback HTTP URL 和内存
  session token，不读取控制令牌，也不自动拉起 daemon。
- `EnsureDaemon(ctx, configPath)`：先探测 `/api/v1/agents`（500ms 超时）；仅网络不可达时自动拉起，认证失败不得启动第二个 daemon。
- 所有错误转换为 `ErrDaemonUnreachable`（区别于业务错误），CLI 据此提示用户。

## 约束

- 禁止在本包解析 agent 状态机/事件结构——只做 JSON 透传。
- 自动拉起只允许出现在 `EnsureDaemon`；其它路径不得隐式启动进程。
- 导出类型：`Client`、`ErrDaemonUnreachable`。
