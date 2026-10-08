# AGENTS.md — internal/config

## 职责

**配置加载与校验**。daemon 与 CLI 共享同一份配置结构，避免各组件各自读环境变量造成漂移。

## 关键设计

- `Config` 结构体是唯一配置模型；`Load(path)` 读取 JSON，空路径解析为 `~/.drove/config.json`，缺失文件或字段使用默认值。
- `LoadResolved(path)` 同时返回绝对配置路径，供 CLI 自动拉起 daemon 时精确透传。
- `Defaults()` 提供安全默认值（数据目录、loopback API 地址、事件缓冲大小、本地控制台
  Origin、30 天原始输出保留期）；`storage.output_retention_days=0` 表示永久保留。
- `Validate()` 以 `0700` 创建缺失的数据目录，但不修改既有目录模式；负保留天数无效。
- `Agents` 保存 vendor-keyed 启动配置；`signal_injection` 只接受 `auto|off`，
  adapter 能力默认值由 composition root 解析，config 不包含厂商分支。
- `session.auto_resume_on_start` 默认关闭；开启后 daemon 只恢复重启前非终态且已有
  vendor ref 的会话。
- `session.termination_grace_seconds` 默认 5；零值仍由 PTY 回退到 5 秒，负值
  在 daemon 启动前被拒绝。
- `notify` 保存 Blocked 通知策略和 Web Push/ntfy 渠道开关；VAPID 私钥与
  ntfy token 不进入配置文件。
- ntfy 远端地址必须使用 HTTPS；HTTP 只允许 loopback。token 只允许引用绝对
  `0600` 文件路径。
- `Validate()` 在启动早期校验；API 只允许 loopback 监听。HTTP Origin 只允许
  loopback，显式配置的远程 Origin 必须使用 HTTPS。
- `disable_tcp=true` 只关闭供浏览器使用的 loopback TCP listener；daemon 的 Unix
  socket 控制面始终启用。

## 约束

- 禁止在其它包硬编码路径/端口常量；一律从 Config 读取。
- 环境变量覆盖（如 `DROVE_DATA_DIR`）只允许在本包实现。
- 导出类型：`Config`、`AgentConfig`、`SignalInjection`、`NotifyConfig`、
  `WebPushConfig`、`NtfyConfig`。
