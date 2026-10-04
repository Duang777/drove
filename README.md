<p align="center"><a href="README.en.md">English</a></p>

<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/assets/logo-dark.svg">
    <img alt="drove" src="docs/assets/logo.svg" width="420">
  </picture>
</p>

<p align="center">
  <strong>跨厂商 AI 编码 agent 的黑匣子与塔台。</strong><br>
  在本机把每一路终端录成可回放的字节，并标出 Working、Blocked、Done、Idle。
</p>

<p align="center">
  <a href="https://github.com/Duang777/drove/actions/workflows/ci.yml"><img alt="CI" src="https://github.com/Duang777/drove/actions/workflows/ci.yml/badge.svg"></a>
  <a href="https://go.dev/dl/"><img alt="Go 1.24.2+" src="https://img.shields.io/badge/Go-1.24.2%2B-00ADD8?logo=go&logoColor=white"></a>
  <a href="LICENSE"><img alt="Apache-2.0" src="https://img.shields.io/github/license/Duang777/drove"></a>
</p>

Drove 是一个本地 daemon 加 CLI。它在真实 PTY 里启动 Claude Code、Codex，或任何一个可执行文件，把原始终端字节追加进 SQLite，并用同一套状态看它们。

黑匣子是已经能用的部分：`drove log` 回放字节。塔台网格、时间线拖动、推送和手机审批在 [Epic #31](https://github.com/Duang777/drove/issues/31)，还没有界面。仓库没有发布包，也没有 TUI。

## 功能状态

| 标记 | 含义 |
| --- | --- |
| 已落地 | 当前 `main` 可以按下面的命令使用 |
| 进行中 | 代码已部分合入，对应 issue 仍打开 |
| 规划中 | 还没有可用的命令或界面 |

| 能力 | 状态 | 在哪里 |
| --- | --- | --- |
| 每个 agent 一个 PTY，由 `droved` 持有 | 已落地 | `internal/pty` |
| `init` `up` `ps` `log` `explain` `stop` `send` `hook` `version` | 已落地 | `cmd/drove` |
| Claude / Codex 按会话注入状态上报 | 已落地 | [#15](https://github.com/Duang777/drove/issues/15) |
| 原始终端字节，默认保留 30 天 | 已落地 | [#13](https://github.com/Duang777/drove/issues/13) |
| `drove log` 回放字节，`--plain` 去掉控制序列 | 已落地 | |
| 只监听 loopback，REST / WebSocket 使用本地令牌 | 已落地 | |
| WebSocket 事件流，以及带 `request_id` 的输入 | 已落地 | |
| Web 开发骨架：列表、启动、停止、实时事件 | 已落地 | `web/` |
| 终端屏幕仿真、查询应答、屏幕规则和 `drove explain` | 已落地 | [#14](https://github.com/Duang777/drove/issues/14)，[spec 010](specs/010-terminal-screen-detection/spec.md) |
| 回放时间线 | 规划中 | [#25](https://github.com/Duang777/drove/issues/25) |
| 塔台网格 | 规划中 | [#26](https://github.com/Duang777/drove/issues/26) |
| 推送通知 | 规划中 | [#27](https://github.com/Duang777/drove/issues/27) |
| 手机上批准、拒绝或回一句 | 规划中 | [#28](https://github.com/Duang777/drove/issues/28) |
| 原生 resume，停止改为 SIGTERM 后宽限再 SIGKILL | 规划中 | [#16](https://github.com/Duang777/drove/issues/16) |
| daemon 退出后 agent 进程仍在 | 规划中 | [#17](https://github.com/Duang777/drove/issues/17)、[#18](https://github.com/Duang777/drove/issues/18) |
| `drove attach`、终端 UI、xterm.js | 规划中 | [#20](https://github.com/Duang777/drove/issues/20) |
| 离开简报、跨会话搜索、git worktree | 规划中 | [#29](https://github.com/Duang777/drove/issues/29)、[#30](https://github.com/Duang777/drove/issues/30)、[#23](https://github.com/Duang777/drove/issues/23) |

## 架构

<p>
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/assets/architecture-dark.png">
    <img alt="架构图。drove CLI 经 REST 与 Bearer 连接 droved，web 开发骨架经 Vite 代理连接 droved。droved 连接 session。session 连接 detect、SQLite 事件日志和 PTY。PTY 连接 claude、codex 或可执行文件。agent 经 DROVE_SIGNAL 连接 drove hook，hook 再经 loopback 与会话 token 回到 droved。" src="docs/assets/architecture.png" width="720">
  </picture>
</p>

CLI 是短命令。`droved` 被自动拉起后一直持有 PTY。状态和输出先写入 SQLite，再经 Hub 推给 WebSocket 订阅者。厂商差异只在 `internal/adapter`。

每个 attached 会话还有一个 terminal actor。它只接收已经写入 SQLite 并发布到
Hub 的输出块，再更新私有终端屏幕。终端查询应答直接写回同一个 PTY，不进入输出
事件或用户输入审计。

## 快速开始

需要 Go 1.24.2 或更高版本。PTY 依赖 `github.com/creack/pty`，在 Linux 和 macOS 上构建；Windows 不在支持范围内。CI 在 Ubuntu 上跑 Go 1.24.2 和当前 stable。仓库还没有 release，请从源码构建。`drove` 和 `droved` 必须放在同一目录：CLI 在旁边找 daemon，daemon 在旁边找 hook relay。

```bash
git clone https://github.com/Duang777/drove.git
cd drove
make build
export PATH="$PWD/bin:$PATH"

drove init
drove up /bin/cat --name demo
drove ps
drove send <agent-id> 'hello'
drove log <agent-id>
drove log <agent-id> --plain
drove explain <agent-id>
drove stop <agent-id>
drove version
```

`drove init` 把默认配置写到 `~/.drove/config.json`。文件已存在时会覆盖。`data_dir` 写成主目录下 `.drove` 的绝对路径，目录权限是 `0700`。

`drove up` 在 daemon 没在听的时候拉起 `droved`。日志在 `<data_dir>/drove.log`。上面的 `/bin/cat` 不需要安装 Claude 或 Codex，用来确认链路。Claude Code 和 Codex 需要它们自己的 CLI 已在 `PATH` 里：

```bash
drove up claude --name api --dir "$PWD"
drove up codex --oneshot
drove up claude --hooks required
```

### 命令

| 命令 | 行为 |
| --- | --- |
| `drove init` | 写入默认 `config.json`。已存在则覆盖 |
| `drove up <vendor\|command>` | 启动一个会话。厂商名是 `claude`、`codex`；其他字符串当作可执行文件名，不能再跟参数 |
| `drove ps` | 打印 AGENT ID、NAME、VENDOR、MODE、STATE、PID。没有会话时打印 `no agents running` |
| `drove log <agent-id>` | 只把终端字节写到 stdout。不打印状态事件 |
| `drove log <agent-id> --plain` | 用流式清洗器去掉控制序列 |
| `drove explain <agent-id>` | 打印最近的状态决策和 attached 会话的临时受限屏幕 |
| `drove send <agent-id> <text>` | 发送这一行并自动加上换行。stdout 打印字节数 |
| `drove send <agent-id> --stdin` | 原样读取标准输入，不追加换行 |
| `drove stop <agent-id>` | 停止该会话 |
| `drove hook --vendor claude\|codex` | 给被注入的 agent 子进程用。从 stdin 读一份 JSON，失败也返回 0 |
| `drove version` | 打印版本。`make build` 用 `git describe` 填版本号；commit 和构建时间未注入时是 `unknown` |

`drove up` 的标志：`--name`、`--dir`、`--oneshot`、`--hooks off|auto|required`。

`drove send` 只接受合法 UTF-8，单次最多 64 KiB。审计事件只记字节数，不记正文。`drove hook` 的单份 JSON 上限是 1 MiB。它不读取控制令牌，也不会拉起 daemon。

## 工作原理

### 状态

状态机在 `internal/agent`。运行中会看到 `working`、`blocked`、`done`、`idle`，另外有生命周期状态 `pending`、`starting`、`stopped`。`done` 和 `stopped` 是终态。

一次决定由会话里的 Detector 算出，先写入 SQLite，再更新内存，最后广播。来源按这个顺序生效：

1. **进程。** 启动失败和退出覆盖其他信号。交互会话结束为 `stopped`。`--oneshot` 成功退出为 `done`。
2. **Claude command hook。** 第一个合法 hook 信号提交后，该会话进入 hook 权威。进入 Working、Blocked 或 Idle 候选由事件种类决定。Idle 有 1 秒确认窗口，后续活动可以取消它。
3. **Codex notify。** 不进入 hook 权威，也不满足 `--hooks required`。只在 fallback 里作为可取消的 Idle 候选。
4. **屏幕规则。** hook 还没激活时，fallback 使用 Claude 和 Codex 的稳定屏幕规则。hook 已经激活时，只允许两类屏幕信号改变状态：审批框消失（Blocked → Working），以及 Claude 中断（Working → Idle）。其他屏幕边沿仍写入事件日志，但结果是 `suppressed`。

`--hooks auto` 是 Claude 和 Codex 的默认值，等待 5 秒。没有合法原生 hook 就进入 fallback。fallback 里，输出静默 60 秒可以成为 Idle 候选。`--hooks off` 不注入上报。`--hooks required` 在 5 秒内没有合法原生 hook 时把会话停为 `stopped`。generic 不能选 `required`。

### 终端屏幕与解释

每个 attached 会话有一个 terminal actor。它独占固定版本的
`github.com/charmbracelet/x/vt`、厂商分类器、100 ms 固定采样计时器和当前不可变
快照。输出必须先写入 SQLite，再更新内存投影并发布到 Hub。只有这次提交返回的
offset、最终事件序号和提交时间才能随字节进入 terminal actor。

x/vt 生成的 DSR、OSC 10/11、DA1 和 Kitty keyboard 查询应答直接调用
`pty.Session.Write`。这个调用与用户输入共用 PTY 的完整帧写锁，但不经过
`Manager.SendInput`，因此不会产生 `agent.input`。reply 也不会进入输出事件、
回放、快照或 explain。子进程自己回显 reply 时，那份回显才是普通 PTY 输出。

屏幕事件只保存稳定规则名、边沿、区域、静态 evidence、输出 offset 和最终输出
序号。事件不保存匹配文本、屏幕行或屏幕 hash。用以下命令查看这些决策：

```bash
drove explain <agent-id>
drove explain <agent-id> --limit 20
drove explain <agent-id> --json
```

attached 会话最多返回底部 12 行，每行最多 160 个 cells，编码后最多 4 KiB。
输出处理器会等长打码当前会话的 signal token，但快照没有通用密钥扫描。屏幕可能
包含源码、prompt 或凭据，只在当前进程 attached 时临时返回。进程 detach 后，
`explain` 只返回持久化的脱敏决策。

当前没有公开的 attach 或 resize API。WebSocket 终端流由
[#19](https://github.com/Duang777/drove/issues/19) 跟踪，Web attach 和终端 UI
由 [#20](https://github.com/Duang777/drove/issues/20) 跟踪。32 会话实测结果见
[技术笔记](docs/technical-notes.md#9-terminal-actor-32-session-benchmark)。

### 会话级注入

默认只改 Drove 启动的那个进程，不改 `~/.claude`、`~/.codex` 或项目配置，也不代替你接受 workspace trust 或 hook trust。

- Claude Code：在 `<data_dir>/sessions/<agent-id>/claude-settings.json` 写入仅含 hooks 的临时文件，权限 `0600`，目录 `0700`，用 `--settings` 加载。进程退出后删除该目录。
- Codex：追加 `-c notify=[...]`，不写配置文件。notify 只表示一轮结束。

两者都继承 `DROVE_AGENT_ID`、`DROVE_SIGNAL_URL`、`DROVE_SIGNAL_TOKEN`。signal 端点只接受 loopback 和这个会话 token。事件日志不保存原始 payload、prompt、tool input、transcript 或 token。

调用方自己带了 Claude `--bare`、`--settings`，或 Codex 的 `notify` 时，Drove 不覆盖，并把这次注入记为跳过。找不到 `drove` relay 时，`auto` 仍会启动。手工配置见 [状态 hook 配置指南](docs/hooks.md)。持久安装器在 [#24](https://github.com/Duang777/drove/issues/24)，尚未实现。Codex OSC 9 通知也还没有注入。

### 记录

新会话把 PTY 输出写成带字节偏移的 `output.chunk`。旧库里的 `output` 行事件仍可读，回放时每行补一个换行。`drove log` 默认保留 ANSI 和无效字节。过期附件不打印占位文本。

原始输出默认保留 30 天。设为 `0` 表示永久保留。清理只删除字节附件，事件序号、时间、offset 和长度都留着。清理打开 SQLite `secure_delete` 并截断 WAL，不执行 `VACUUM`，所以库文件已经占住的空间可能不缩小。

数据库里一旦有 `output.chunk`，可回滚的最低提交是 [`d11f6c3`](https://github.com/Duang777/drove/commit/d11f6c3)。屏幕证据 version 3 写出之后，可回滚的最低提交是 [`f361ab5`](https://github.com/Duang777/drove/commit/f361ab5)。

### 停止和重启

`drove stop` 以及 daemon 收到 SIGINT / SIGTERM 后的关闭，都对 PTY 子进程调用 `Process.Kill`（SIGKILL）。没有 SIGTERM 宽限。

`drove up` 返回之后，前台命令已经结束，会话挂在 `droved` 上。这个 daemon 没有脱离控制终端，也不处理 SIGHUP。daemon 退出后不会留下 agent 进程。

daemon 再次启动时从事件日志恢复投影。无法重连的旧会话被收口为 `stopped`，原因是 `session interrupted by daemon restart; previous PTY is not reconnectable`。已经写下的字节还在，可以用 `drove log` 看。活着的进程不会回来。

原生 `claude --resume` / `codex resume`，以及先 SIGTERM 再宽限、最后 SIGKILL，在 [#16](https://github.com/Duang777/drove/issues/16)。每会话 shim、daemon 重启后进程仍在，在 [#17](https://github.com/Duang777/drove/issues/17) 和 [#18](https://github.com/Duang777/drove/issues/18)。

## 支持的 agent

| 启动 | 交互模式 | `--oneshot` | 状态信号 |
| --- | --- | --- | --- |
| `drove up claude` | `claude` | `claude --print` | 会话级 command hooks |
| `drove up codex` | `codex` | `codex exec` | 进程级 notify，只在 fallback 确认 Idle |
| `drove up <可执行文件>` | 直接执行该文件，没有额外参数 | 成功退出为 `done` | 无 hook，屏幕分类器为空，不能 `--hooks required` |

ACP 没有注册。`drove up acp` 会去执行一个名叫 `acp` 的程序，而不是 ACP 适配器。

## 配置

配置文件是 `~/.drove/config.json`。`DROVE_DATA_DIR` 覆盖 `data_dir`。daemon 拒绝非 loopback 的 `api_bind`。

```json
{
  "data_dir": "/home/you/.drove",
  "api_bind": "127.0.0.1:7373",
  "event_buffer": 1024,
  "console_origins": [
    "http://localhost:5173",
    "http://127.0.0.1:5173"
  ],
  "storage": {
    "output_retention_days": 30
  }
}
```

`agents.<vendor>.signal_injection` 只接受 `auto` 或 `off`。这是厂商默认注入开关。`off|auto|required` 是单次 `drove up --hooks` 的会话策略，不写在这个字段里。

```json
{
  "agents": {
    "claude": {"signal_injection": "off"},
    "codex": {"signal_injection": "off"}
  }
}
```

控制令牌是 256 位随机值，十六进制写在 `<data_dir>/control.token`，权限 `0600`。首次启动 daemon 时生成。配置文件本身是 `0644`。

## Web 开发骨架

daemon 不托管前端。Vite 开发服务器把 `/api` 和 `/ws` 代理到 `api_bind`，并从 `control.token` 注入 Bearer。先让 daemon 起来，否则令牌文件还不存在：

```bash
drove ps
cd web
npm install
npm run dev
```

打开 `http://127.0.0.1:5173`。页面可以列出、启动、停止会话，并显示 WebSocket 事件。`output.chunk` 只显示 offset 和长度，不画终端。页面上的类名还没有接入样式构建。回放字节用 `drove log`。

实时终端、回放拖动和塔台网格属于 [#20](https://github.com/Duang777/drove/issues/20) 和 [#26](https://github.com/Duang777/drove/issues/26)。产品方向把 #20 定为 Web 优先。

## 路线图

已批准的 MVP 是 [Epic #31：黑匣子 + 塔台](https://github.com/Duang777/drove/issues/31)。

1. [#19](https://github.com/Duang777/drove/issues/19) WebSocket 终端流
2. [#25](https://github.com/Duang777/drove/issues/25) 回放时间线
3. [#20](https://github.com/Duang777/drove/issues/20) Web 实时终端与回放
4. [#26](https://github.com/Duang777/drove/issues/26) 塔台网格
5. [#21](https://github.com/Duang777/drove/issues/21) unix socket、Host 校验、cookie、令牌轮换
6. [#27](https://github.com/Duang777/drove/issues/27) 推送，[#28](https://github.com/Duang777/drove/issues/28) 手机上的批准 / 拒绝 / 回复
7. [#16](https://github.com/Duang777/drove/issues/16) 原生 resume 与更温和的停止

MVP 之后是 [#17](https://github.com/Duang777/drove/issues/17) / [#18](https://github.com/Duang777/drove/issues/18) 的 shim，然后是 [#29](https://github.com/Duang777/drove/issues/29) 离开简报、[#30](https://github.com/Duang777/drove/issues/30) 全文搜索、[#23](https://github.com/Duang777/drove/issues/23) worktree。[#22](https://github.com/Duang777/drove/issues/22) 结构化状态源和 [#24](https://github.com/Duang777/drove/issues/24) 持久 hook 安装器推迟。

## 和其他工具的差别

Drove 跑的是厂商自己的 CLI，不接它们的私有 SDK。它现在提供的是本地事件日志、字节回放，以及 Claude 与 Codex 共用的状态命令。它不提供 git worktree、diff 审阅或 PR 流程。

[herdr](https://herdr.dev/) 以 TUI 为中心，公开定位是关掉客户端后由后台 server 继续持有终端。Drove 把原始字节和状态事件留在本机 SQLite 里。daemon 退出后进程仍在，不是 Drove 今天的行为。单厂商的后台会话、手机审批和官方 App，各自只覆盖自己的 agent。

## 安全

这是一个单用户、本机控制面。

- `api_bind` 必须是 loopback。非 loopback 地址在配置校验时被拒绝。
- REST 和 WebSocket 需要 `Authorization: Bearer`，令牌来自 `control.token`。比较是常量时间的。
- WebSocket 没有 `Origin` 时放行（CLI）。有且仅有一个 `Origin` 时，必须精确匹配 `console_origins`。
- `/signal` 只接受 loopback 和该会话的 token，不接受控制面令牌。
- 同一 OS 用户能读到令牌文件。令牌不防本机上的其他进程，也不防 agent 自己。
- 输入审计不保存正文。原始输出可能含有源码和密钥，默认 30 天后删除附件。
- 输出流里的 signal token 会按等长方式打码。
- `drove explain` 的 live screen 没有通用密钥扫描，只在会话 attached 时返回。
- 没有自动批准。手机上的批准动作在 [#28](https://github.com/Duang777/drove/issues/28)，默认也不会自动同意。

设计说明在 [RFC-001 的安全考虑](docs/rfc-001-agent-state-and-control.md)。仓库没有单独的威胁模型文件。

## 文档

| 文档 | 内容 |
| --- | --- |
| [状态 hook 配置指南](docs/hooks.md) | 会话注入和手工原生 hooks |
| [RFC-001](docs/rfc-001-agent-state-and-control.md) | 状态、输入和安全设计 |
| [spec 006](specs/006-hook-backed-state-detection/spec.md) | hook 状态检测 |
| [spec 008](specs/008-session-signal-injection/spec.md) | 按会话注入 |
| [spec 009](specs/009-raw-output-chunks/spec.md) | 原始字节与保留期 |
| [spec 010](specs/010-terminal-screen-detection/spec.md) | 屏幕检测、查询应答和解释命令 |
| [技术笔记](docs/technical-notes.md) | 阶段性阅读笔记。文首说明前六节不代表当前主干 |
| [AGENTS.md](AGENTS.md) | 目录职责和工程约束 |

## 参与贡献

每个目录有自己的 `AGENTS.md`，以最近的一份为准。提交说明使用 Conventional Commits：`feat:`、`fix:`、`refactor:`、`docs:`、`test:`。

```bash
make test   # go test ./... -race，并打印覆盖率摘要
make vet
make lint   # gofmt + vet
```

前端类型检查和构建：

```bash
npm --prefix web run typecheck
npm --prefix web run build
```

## 许可

[Apache-2.0](LICENSE)。
