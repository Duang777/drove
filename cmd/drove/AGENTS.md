# AGENTS.md — cmd/drove

## 职责

**CLI 主程序入口**（daemon 客户端模式）。提供子命令（init / up / ps / log / stop / version），所有会话操作都经 `internal/client` 与常驻 daemon 通信；daemon 未运行时自动拉起（docker 式体验）。

## 关键设计

- 命令树用 `spf13/cobra`；`root` 只挂子命令，不做业务。
- `drove init` 生成默认配置到 `~/.drove/config.json`。
- `drove up <vendor|command>` 启动一个 agent：默认 `interactive`，`--oneshot` 切换为单次执行；未知厂商名仍视为 generic 命令。
- `drove send <id> <text>` 向运行中的 agent 发送一行输入；`--stdin` 保留标准输入的原始换行。
- `drove ps` 列出全部会话；`drove log <id>` 回放事件流；`drove stop <id>` 停止（幂等）。
- 每次命令从 DataDir 读取控制令牌后调用 `client.EnsureDaemon`；仅网络不可达时后台拉起 `droved`，认证失败直接返回。
- `version` 子命令输出 `internal/version` 注入信息。

## 约束

- main 必须保持极薄：解析参数 → 调用 client → 退出码语义化（0 成功 / 1 用户错误 / 2 运行时错误）。
- 禁止在 cmd 中复制业务逻辑或直接构造 session/pty/store；一律走 `internal/client`。
- 本地自定义类型必须与 daemon API 的 JSON 契约一致（增删字段需同步 API 层测试）。
- 导出符号：无（main 包不导出）。
