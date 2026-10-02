# Drove 技术笔记

本文记录以提交 `50214ba` 为基线的代码阅读和本地验证结果，并补充重启安全事件序号与会话投影恢复的验证证据。它描述当前实现，不把 `README.md` 或 `AGENTS.md` 中的规划当成已经完成的功能。

## 1. 痛点与目标

Drove 想解决的问题是：多个 coding agent 各自在终端里运行，用户缺少统一的启动、观察、状态判断和历史回放入口。

项目选择了一个本地 control plane：

- `droved` 常驻并管理 agent 子进程。
- `drove` 通过 HTTP 调用 daemon。
- React 控制台通过 REST 和 WebSocket 展示状态与事件。
- 每个 agent 运行在 PTY 中，Drove 不接入厂商私有 SDK。
- 状态变化和输出转换成事件，先写 SQLite，再广播给实时订阅者。

这个方向适合本地开发工具。厂商差异被限制在 adapter 包中，核心会话模型不依赖 Claude Code 或 Codex 的 SDK。

## 2. 当前完成度

当前仓库是一个初始 MVP，共 59 个受 Git 管理的文件，只有一个提交、一个分支，没有 tag 和公开 issue。

### 已经实现

- 两个可编译的 Go 二进制：`drove` 和 `droved`。
- Agent 状态机及合法迁移表。
- Claude、Codex 和 generic 三类 adapter。
- PTY 子进程启动、按行读取、写入和关闭。
- SQLite 追加式事件日志及按 session 回放。
- daemon 启动时从 SQLite 事件流恢复历史会话，并从提交后的最大序号继续分配。
- 无法重连 PTY 的历史会话会追加恢复事件并收口为 `stopped`。
- 新会话会先持久化名称和厂商元数据，再进入状态机。
- REST 管理接口和 WebSocket 实时事件流。
- CLI 的 `init`、`up`、`ps`、`log`、`stop`、`version` 命令。
- React 控制台骨架、REST 客户端和 WebSocket 自动重连。

### 尚未形成完整产品闭环

- 没有可用的交互式 TUI。代码使用 Cobra，不包含 Bubble Tea 依赖。
- API 没有输入注入或终端 resize 端点，Blocked agent 无法通过 Drove 恢复。
- Web 控制台没有接入样式系统，现有 Tailwind 类不会生成 CSS。
- Web 控制台没有可达的回放入口。
- ACP 仍是文档中的预留项。
- CI 只验证 Go，不验证 Web 构建或类型检查。

## 3. 实际架构

```text
CLI: cmd/drove
      |
      v
internal/client
      |
      | HTTP
      v
internal/api  <--------------------------- web/
      |                                     | REST
      |                                     | WebSocket
      v                                     |
internal/session.Manager -------------------+
      |
      +--> internal/adapter --> vendor command
      |
      +--> internal/pty ------> child process
      |
      +--> internal/agent ----> state machine
      |
      +--> internal/store ----> SQLite event log
      |
      +--> internal/event ----> live subscribers
```

`internal/session.Manager` 是当前实现的中心。它持有 agent、PTY session、adapter registry、event hub 和 store。

### 启动链路

`POST /api/v1/agents` 最终调用 `Manager.Start`：

1. 根据 `vendor` 查找 adapter。
2. 根据 adapter 或请求参数解析并校验命令。
3. 创建 `agent.Agent`，初始状态为 `pending`。
4. 写入包含名称和厂商的 `session_lifecycle(created)` 事件。
5. 迁移到 `starting`，状态事件写入 SQLite。
6. 启动 PTY 和子进程。
7. 注册输出与退出回调。
8. 迁移到 `working`。
9. 返回 `session.Status`。

关键代码：

- [`internal/api/server.go`](../internal/api/server.go)
- [`internal/session/session.go`](../internal/session/session.go)
- [`internal/pty/pty.go`](../internal/pty/pty.go)
- [`internal/adapter/adapter.go`](../internal/adapter/adapter.go)

### 事件链路

每次状态变化或输出到达时，`Manager.persistAndPublish` 执行：

```go
ev.Seq = m.hub.NextSeq()
store.AppendEvent(ev)
hub.Publish(ev)
```

设计意图是先落库，再广播。REST 回放查询 SQLite，WebSocket 消费 Hub 的实时事件。

关键代码：

- [`internal/session/session.go`](../internal/session/session.go)
- [`internal/event/event.go`](../internal/event/event.go)
- [`internal/store/store.go`](../internal/store/store.go)

### 状态模型

状态机包含：

```text
pending -> starting -> working
                         |
                         +--> blocked -> working
                         +--> done
                         +--> idle -> working

任意运行态最终进入 stopped
```

adapter 只返回 `StateHint`。`Manager` 决定是否执行迁移。这个边界是合理的，但当前 `Confidence` 字段没有参与决策。

### Web 数据链路

前端每 5 秒调用一次 `GET /api/v1/agents`，同时订阅 `/ws`。实时事件只用于本地状态投影，daemon 的列表响应仍是权威状态。

这套双轨模型能容忍 WebSocket 丢事件，但当前前端没有运行时 schema 校验。除 `hello` 消息外，任意 JSON 都会被直接断言成 `Event`。

## 4. 已验证的基线

本地环境：

- Go `1.24.13`
- Node `24.16.0`
- npm `11.13.0`
- macOS arm64

验证结果：

| 命令 | 结果 |
| --- | --- |
| `make build` | 通过，生成两个二进制 |
| `make vet` | 通过 |
| `make test` | 通过，启用 race detector |
| Go 总覆盖率 | 19.8% |
| `npm ci` | 通过，0 个已知漏洞 |
| `npm run typecheck` | 通过 |
| `npm run build` | 通过 |

Go 覆盖率集中在 `agent`、`event`、`adapter` 和 `store`。以下主链路包的覆盖率都是 0%：

- `session`
- `pty`
- `api`
- `client`
- `daemon`
- 两个 `cmd`

前端没有测试，也没有进入 GitHub Actions。

## 5. 实测发现

### 已修复：daemon 重启后事件持久化中断

测试过程：

1. 使用隔离数据目录启动 daemon。
2. 运行两个 generic 短命令。
3. 确认 SQLite 中有序号 1 到 7 的事件。
4. 正常停止并使用同一数据目录重启 daemon。
5. 再启动一个 generic 短命令。

结果：

- 历史事件仍可通过已知 session ID 回放。
- `GET /api/v1/agents` 在重启后返回空数组。
- 新 agent 能运行，状态也会变为 `stopped`。
- 新 session 的回放结果是 `null`。
- SQLite 仍只有原来的 7 条事件。

根因是 `event.NewHub()` 每次从序号 0 开始，而 daemon 没有读取 `Store.LastSeq()`。新事件再次使用序号 1，SQLite 主键冲突。`persistAndPublish` 吞掉持久化错误，只向实时 Hub 发布一个 error 事件。

修复后 `event.NewHub(initialSeq)` 显式接收当前序号。daemon 在创建 Manager 和 API listener 之前读取 `Store.LastSeq()`，读取失败则终止启动，不会退回序号 0。

回归验证使用隔离数据目录和同一个 SQLite 文件：

1. 第一次启动 daemon 后运行一个 generic 短命令，数据库写入序号 1 到 3。
2. 停止并重启 daemon。
3. 再运行一个 generic 短命令，新会话成功回放序号 4 到 6。
4. SQLite 最终包含 6 条连续事件，`MIN(seq)=1`，`MAX(seq)=6`。

单元测试同时覆盖空库从 1 开始、有历史时从 42 续接，以及最大序号读取失败时返回带 daemon 和 store 上下文的错误。

修复后的验证结果：

| 命令 | 结果 |
| --- | --- |
| `go test ./internal/event ./internal/daemon -race` | 通过 |
| `go test ./... -race` | 通过 |
| `go vet ./...` | 通过 |
| `make build` | 通过，生成 `bin/drove` 与 `bin/droved` |
| 隔离数据目录两次启动回归 | 通过，重启前最大序号 3，重启后新会话序号为 4 到 6 |

### 已修复：daemon 重启后会话投影丢失

daemon 现在会在创建 API server 和监听端口前完成以下步骤：

1. 按全局序号流式扫描 SQLite 事件。
2. 重建每个 session 的名称、厂商、状态、错误和时间。
3. 将当前 daemon 无法控制的历史非 stopped 会话追加为 stopped。
4. 在一个事务中提交全部 reconciliation 事件。
5. 用提交后的最大序号创建 Hub，再构造对外 API。

旧数据库没有 `created` 事件时，恢复结果使用完整 Agent ID 作为名称，并将厂商标记为 `unknown`。只有 output 或 error、没有生命周期或状态事实的 session 不会出现在状态列表中。坏状态链、坏元数据和未知事件类型会阻止 daemon 监听端口。

三次启动回归使用隔离数据目录 `/tmp/drove-three-start.6H6Kar`：

1. 第一次启动创建一个 1 秒短任务和一个仍在运行的任务。短任务正常 stopped，长任务保持 working，SQLite 序号为 1 到 7。
2. 第二次启动扫描 7 条事件，恢复 2 个会话，将长任务追加 error 和 `working -> stopped`，最大序号变为 9。两个恢复状态都没有 PID。
3. 第二次启动后创建的新任务从序号 10 开始，正常结束于序号 13。
4. 第三次启动扫描 13 条事件，恢复 3 个 stopped 会话，`interrupted=0`，没有重复追加 reconciliation，SQLite 仍为 13 条事件。

验证结果：

| 命令或场景 | 结果 |
| --- | --- |
| `go test ./internal/session ./internal/store ./internal/agent ./internal/event ./internal/daemon -race` | 通过 |
| `go vet ./...` | 通过 |
| `make build` | 通过 |
| 隔离数据目录三次启动回归 | 通过，恢复序号 8 到 9，新会话从 10 开始，第三次启动最大序号保持 13 |

### P0：配置初始化与配置加载没有接通

`drove init` 写入 `~/.drove/config.json`。但是 CLI 和 daemon 都调用 `config.Load("")`，而 `Load` 只有在 `path != ""` 时才读取文件。

因此用户修改 `api_bind`、`event_buffer` 或 `db_path` 后，默认启动路径不会读取这些值。当前真正生效的默认覆盖只有 `DROVE_DATA_DIR`。

### P0：交互闭环缺失

`session.Manager.Write` 和 `pty.Session.Resize` 已经存在，但 API、client、CLI 和 Web 都没有暴露对应操作。

adapter 可以把状态识别成 `blocked`，但用户不能通过 Drove 向该 PTY 输入内容。此时只能绕过 Drove 操作原终端，而 daemon 模式没有暴露原终端。

### P1：进程回调存在启动竞态

`pty.Start` 先启动 `readLoop` 和 `waitLoop`，`Manager.Start` 返回后才设置 `OnOutput` 和 `OnExit`。短命令可能在回调注册前输出或退出。

本次开发在给成功启动路径增加 race 测试时复现了该竞态。恢复功能没有修改 PTY 生命周期，因此该问题仍需独立修复；`pty` 目前也没有自己的测试。

### P1：daemon 关闭没有按文档清理会话

`daemon.Run` 只关闭 HTTP server，并通过 `defer` 关闭 store。它没有停止 `Manager` 中的 PTY session，也没有关闭 Hub。

这与 `internal/daemon/AGENTS.md` 描述的关闭顺序不同。daemon 退出后，子进程可能成为孤儿，仍在运行的回调也可能继续访问已经关闭的 store。

### P1：Web 控制台目前是无样式骨架

组件大量使用 Tailwind class，但项目没有 Tailwind 依赖、配置或 CSS 入口。Vite 构建产物只有 HTML 和 JS，没有 CSS 文件。

另外还有两个功能断点：

- 选择 `generic` 后只发送 `{vendor: "generic"}`，后端会拒绝，因为 generic 必须提供 command。
- `EventLog` 支持 `replayID`，但 `App` 从不传入该属性，因此界面无法进入回放模式。

### P2：其他实现缺口

- `StateIdle` 有迁移规则，但没有任何运行时信号会进入该状态。
- `StateHint.Confidence` 被记录，但没有使用。
- WebSocket 没有文档所说的定时 ping。客户端断开且没有新事件时，服务端订阅可能继续存活。
- `CheckOrigin` 无条件返回 true。默认只绑定 localhost 时风险有限，但配置为外部地址后需要安全策略。
- API 将所有启动失败都映射为 500，没有区分无效请求和运行时故障。
- 前端直接断言 REST 和 WebSocket JSON 类型，没有边界校验。

## 6. 推荐的开发顺序

### 第一阶段：修复持久化正确性

目标是让 daemon 重启后可以继续追加事件，并能从事件日志恢复历史会话视图。

验收条件：

- [x] daemon 启动时从 Bootstrap 提交后的最大序号初始化 Hub。
- [x] 重启后的第一条事件序号严格大于已有最大序号。
- [x] daemon 从历史事件重建 session 状态列表。
- [x] 无法恢复的运行中进程追加恢复事实并标记为 `stopped`。
- [x] 完成隔离数据目录的跨重启回归验证。

这里需要先做产品决策：Drove 是否承诺 daemon 重启后重新连接原进程。普通子进程加 PTY 无法提供该能力。如果要保留进程，需要 tmux、独立 supervisor 或可重连的终端后端。事件溯源只能恢复历史和投影，不能恢复已经丢失的进程句柄。

### 第二阶段：接通交互闭环

增加输入与 resize 的 API、client 和 CLI/Web 调用。状态机还需要定义用户输入后从 `blocked` 回到 `working` 的触发规则。

验收条件：

- 用户能启动一个等待 stdin 的 generic 命令。
- 用户能通过 API 或 CLI 写入一行输入。
- 输出进入事件日志。
- 状态从 `blocked` 回到 `working`，最后进入 `stopped`。

### 第三阶段：修复生命周期和并发

- 在启动 goroutine 前注册 PTY 回调。
- 为 `Manager` 增加统一的 `Close`。
- 明确 API、Hub、PTY 和 store 的关闭顺序。
- 为 `pty`、`api` 和 `client` 增加测试。

### 第四阶段：补齐 Web MVP

- 选择 CSS 方案并接入真实样式产物。
- 给 generic agent 增加 command 和 args 输入。
- 增加 agent 选择与事件回放入口。
- 为 REST 和 WebSocket 消息增加运行时解析。
- 把 `npm run typecheck` 和 `npm run build` 加入 CI。

## 7. 已完成的持久化开发任务

已实现“重启安全的事件序号”和“重启安全的会话投影恢复”。

本次修改：

- `event.NewHub` 要求调用方传入初始序号。
- `session.Bootstrap` 在 daemon 监听前扫描、投影、校验并追加恢复事件。
- `daemon.Run` 使用 Bootstrap 返回的 Manager 和 Hub。
- 新会话持久化 version 1 名称与厂商元数据。
- 测试覆盖空库、legacy 历史、状态链缺口、坏事件、幂等恢复和失败启动。
- 真实三次启动回归确认状态可查询、PID 不恢复、reconciliation 不重复且新事件序号继续增长。

进程重连和运行期持久化失败传播仍是后续任务。事件投影恢复不会重新连接或控制旧进程。

## 8. 面试口述版

实现上，Drove 用一个 Go daemon 管理多个 PTY 子进程。CLI 和 React 控制台都通过 REST 与 WebSocket 访问 daemon。session 层负责组合状态机、adapter、PTY、事件 Hub 和 SQLite。

原理上，它把 agent 输出和状态变化转换成有序事件。SQLite 保存历史，Hub 分发实时事件，前端再从事件流计算展示状态。adapter 只识别厂商输出中的状态提示，最终状态迁移仍由统一状态机决定。

同类工具中，tmux 更关注终端持久化，Temporal 更关注可恢复工作流，LangGraph、CrewAI 和 AutoGen 更关注 agent 编排。Drove 当前选择的是本地进程控制与观察，暂时没有工作流调度、模型 SDK 编排或真正的终端重连能力。

设计取舍上，PTY 和 adapter 隔离降低了厂商锁定，追加式事件日志也适合审计和回放。daemon 现在能从事件重建会话投影，但不能重连旧 PTY，运行期持久化失败也仍可能造成有界序号缺口。后续开发应先修复 PTY 回调注册和关闭顺序，再扩展 UI 和厂商能力。
