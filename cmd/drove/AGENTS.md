# AGENTS.md — cmd/drove

## 职责

**CLI 主程序入口**（daemon 客户端模式）。提供子命令（init / up / ps / log /
resume / timeline / explain / stop / token rotate / web / version），所有会话操作都经
`internal/client` 与常驻 daemon
通信；daemon 未运行时自动拉起（docker 式体验）。

## 关键设计

- 命令树用 `spf13/cobra`；`root` 只挂子命令，不做业务。
- `drove init` 以 `0700` 创建缺失的数据目录，并把含 30 天输出保留期的默认配置写到
  `~/.drove/config.json`。
- `drove up <vendor|command>` 启动一个 agent：默认 `interactive`，`--oneshot`
  切换为单次执行，`--hooks` 选择 `off|auto|required`；未知厂商名仍视为
  generic 命令。
- `drove send <id> <text>` 向运行中的 agent 发送一行输入；`--stdin` 保留标准输入的原始换行。
- `drove hook --vendor <vendor>` 默认从 stdin 读取一个 hook JSON 文档；
  `--payload-argv` 改为读取唯一位置参数，`--managed-by drove/v1` 只作受管命令标记。
  两种模式都使用三个继承的 session 环境变量静默转发到 daemon；不得自动拉起
  daemon，且投递失败只能写脱敏 stderr 并返回成功。
- `drove ps` 列出全部会话；`drove log <id>` 默认只向 stdout 回放终端字节，
  `--plain` 使用流式清洗器移除跨事件控制序列；旧 `output` 事件补一个换行，
  已过期的 `output.chunk` 不输出占位文本；`drove stop <id>` 停止（幂等）。
- `drove resume <id>` 调用厂商原生恢复并保持同一 Agent ID；`drove ps` 的
  `RESUMABLE` 列来自 daemon 派生状态。
- `drove explain <id>` 按时间顺序打印类型化决策；`--limit` 限制最近事件数，
  `--json` 保持 stdout 仅含响应 JSON。只有 attached snapshot 存在时才打印
  `ephemeral redacted current screen` 标签和受限行视图。
- `drove timeline <id>` 打印状态区间和一基 Blocked 跳转列表；`--json` 原样输出
  类型化 timeline 响应。CLI 在本阶段不执行终端播放。
- 每次命令经 DataDir 下的 Unix socket 调用 daemon，并从同一目录读取控制令牌后调用
  `client.EnsureDaemon`；仅 socket 不可达时后台拉起 `droved`，认证失败直接返回。
- `drove token rotate` 经 Unix socket 请求 daemon 原子轮换令牌；命令本身不读取或
  输出令牌值。
- `drove web` 经 Unix socket 签发一次性 code，把 code 放在 `/login` URL fragment
  中交给系统浏览器；成功输出不得包含 fragment。`disable_tcp=true` 时拒绝签发。
- `version` 子命令输出 `internal/version` 注入信息。

## 约束

- main 必须保持极薄：解析参数 → 调用 client → 退出码语义化（0 成功 / 1 用户错误 / 2 运行时错误）。
- 禁止在 cmd 中复制业务逻辑或直接构造 session/pty/store；一律走 `internal/client`。
- 本地自定义类型必须与 daemon API 的 JSON 契约一致（增删字段需同步 API 层测试）。
- 导出符号：无（main 包不导出）。
