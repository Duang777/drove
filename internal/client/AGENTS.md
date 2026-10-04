# AGENTS.md — internal/client

## 职责

**daemon 客户端**：CLI 与 daemon 通信的 HTTP 封装，并负责在 daemon 未运行时自动拉起。

## 关键设计

- `Client` 封装 REST 调用（List / Start / Resume / Status / Stop / CleanupWorktree /
  SendInput / Replay / Explain / Timeline / BlockedOccurrence / Frame / RotateToken /
  IssueLoginCode）；
  CLI 使用 `NewLocal` 经
  `$DataDir/run/droved.sock` 调用，
  TCP 构造器保留给测试与浏览器边界；Explain 仅编码路径、可选 limit 并解码类型化
  响应，Timeline、BlockedOccurrence 和 Frame 解码类型化回放响应，
  RotateToken 与 IssueLoginCode 只调用 local-only 端点。
- 每次请求从配置的数据目录读取控制令牌并发送 Bearer 认证，避免令牌轮换后持有过期值。
- `HookRelay` 是独立的 hook 回调路径，校验精确的 loopback session URL，
  使用内存 token，并为一次投递生成可重试的 delivery ID；存在注入的 socket
  路径时强制经 Unix socket 投递。
- `OpenTerminal` 协商 `drove.v2`，严格解码 text/binary 帧并提供订阅、取消订阅、
  输入和 resize。`TerminalStream.Next` 只在消费回调成功后推进该订阅 cursor，
  回调失败时保留同一帧供重试；`NewLocal` 创建的终端连接复用同一个 Unix socket
  dialer。
- Hook relay 不读取控制令牌或 Drove 配置，也不自动拉起 daemon。
- `EnsureDaemon(ctx, configPath)`：先探测 `/api/v1/agents`（500ms 超时）；仅网络不可达时自动拉起，认证失败不得启动第二个 daemon。
- 自动拉起日志与控制令牌使用同一个 DataDir；缺失目录以 `0700` 创建，新日志文件使用
  `0600`，既有目录模式不自动修改。
- 所有错误转换为 `ErrDaemonUnreachable`（区别于业务错误），CLI 据此提示用户。

## 约束

- 禁止在本包解析 agent 状态机/事件结构——只做 JSON 透传。
- 自动拉起只允许出现在 `EnsureDaemon`；其它路径不得隐式启动进程。
- 导出类型：`Client`、`HookRelay`、`HookRelayConfig`、
  `RemovedWorktree`、
  `TerminalStream`、`TerminalSubscription`、`TerminalMessage`、
  `TerminalStreamError`、`ErrDaemonUnreachable`。
