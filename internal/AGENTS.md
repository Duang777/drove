# AGENTS.md — internal

## 职责

**内部实现包目录**（Go internal 规则：本仓库之外不可引用）。这是 Drove 的全部核心逻辑所在。

## 包与依赖方向（禁止循环依赖）

```
cmd/* ──▶ internal/clitui ──▶ internal/client ──▶ internal/session
                │                   │                  ├──▶ agent
                └──▶ cliattach ─────┘                  ├──▶ event
                                                       ├──▶ store
                                                       ├──▶ pty
                                                       ├──▶ adapter ──▶ detect
                                                       └──▶ workspace
```

- `session` 编排一切：agent + pty + adapter + detect + event + store + workspace。
- `daemon` 装配 session/api/config/store/hub（composition root）。
- `notify` 只依赖 agent/event/store 的稳定类型，独立维护可变通知状态；daemon
  装配其生命周期，API 只依赖窄通知接口。
- `localipc` 封装 Unix listener、peer credential 与客户端 transport，供 daemon
  和 client 依赖。
- `api` 只依赖 `session` 与 `event` 的公开接口。
- `client` 只做 JSON 透传（HTTP 客户端），不解析领域类型。
- `clitui` 只协调 client 公开能力与 `cliattach` 交接；fleet 状态来自
  `session.Status`，只有选中会话持有 snapshot stream。
- `adapter` 依赖 `detect` 的标准信号类型和 `agent` 的运行模式；`detect` 只依赖
  `agent` 快照与 Change。其余叶子包不得反向依赖 `session`。
- `term` 负责无厂商逻辑的流式控制序列清洗、屏幕仿真和有界终端查询应答，
  供 `adapter`、`recording` 和 `session` 复用。
- `workspace` 只依赖 Git 与文件系统，负责 Drove 管理的 worktree 生命周期；CLI
  可直接调用它执行本地 `ls` / `rm`。

## 约束

- 新增包必须先写 `AGENTS.md`（职责、关键设计、约束三节）。
- 包级边界由上面的依赖方向约束；违反即视为架构违规，需评审。
- 禁止跨包访问未导出的符号；禁止用 `//go:linkname` 之类的绕行手段。
