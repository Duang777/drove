# AGENTS.md — internal/client

## 职责

**daemon 客户端**：CLI 与 daemon 通信的 HTTP 封装，并负责在 daemon 未运行时自动拉起。

## 关键设计

- `Client` 封装 REST 调用（List / Start / Status / Stop / SendInput / Replay），基址来自 Config.APIBind。
- `EnsureDaemon(ctx, configPath)`：先探测 `/api/v1/agents`（500ms 超时）；不可达则找到 `droved`（优先可执行文件同目录，其次 PATH），携带同一配置路径后台启动并写日志到 `~/.drove/drove.log`，轮询至多 3 秒等待就绪。
- 所有错误转换为 `ErrDaemonUnreachable`（区别于业务错误），CLI 据此提示用户。

## 约束

- 禁止在本包解析 agent 状态机/事件结构——只做 JSON 透传。
- 自动拉起只允许出现在 `EnsureDaemon`；其它路径不得隐式启动进程。
- 导出类型：`Client`、`ErrDaemonUnreachable`。
