# AGENTS.md - internal/clitui

## 职责

**终端总览界面**：管理 Bubble Tea 状态、fleet 展示投影、选中会话快照生命周期、
本地操作以及向 `cliattach` 的交互式 attach 交接。

## 关键设计

- `session.Status` 是 fleet 的唯一权威来源；每次成功轮询都整表替换本地展示行。
- fleet 过滤和 Blocked 置顶是纯投影，选择同时保存 Agent ID 与回退索引。
- 只有选中的 Agent 持有 snapshot 订阅；preview actor 独占 stream 和 `Next` 调用，
  切换目标时先取消并等待旧 generation 退出。
- Bubble Tea 负责总览终端状态与 resize；attach 通过 `tea.Exec` 暂停总览，并把
  raw mode、输入输出 pump、SIGWINCH 和 Ctrl-Q 交给 `cliattach.Run`。
- 退出总览只清理本地请求、stream 和 worker，不停止远端 Agent。

## 约束

- 只依赖 `internal/client`、`internal/cliattach`、`internal/session`，以及
  `internal/agent`、`internal/detect` 的公开领域类型。
- 禁止依赖 `internal/api`、`internal/store`、`internal/pty`、`internal/term` 或
  `internal/adapter`。
- 禁止从终端文本或原始事件 payload 推断 Agent 状态，也不在本包运行终端模拟器。
- 对外只暴露 `Run(context.Context, *client.Client) error`；Cobra 不持有模型或渲染逻辑。
