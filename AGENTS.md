# AGENTS.md — Drove 项目总纲

> 本文件是给 AI Agent / 协作者的项目指引。每个子目录内都有各自的 `AGENTS.md`，以最近目录的说明为准；冲突时，子目录优先，总纲兜底。

## 1. 项目是什么

**Drove** 是跨厂商 Agent 指挥台（control plane）：让用户在一个持久化工作区里同时运行、观察、回放多个 AI coding agent（Claude Code / Codex / 任意 CLI agent），并对每个 agent 识别状态（Working / Blocked / Done / Idle）。

一句话定位：**herdr 让 Agent 活着，Drove 让它们往对的方向跑。**

## 2. 核心架构原则（不可违反）

1. **每 Agent = 一个 goroutine + 一个 PTY**：Agent 必须运行在真实终端（PTY）中，Drove 只做包装、观察、注入，不替代 agent 本体。
2. **事件驱动一切**：所有状态变化、输出增量、错误都以不可变事件（`internal/event`）表达，经 Hub 扇出到订阅者（daemon API / CLI）。
3. **事件溯源优先**：会话是可回放的事件流，不是可变状态快照。状态由事件派生（projection），存储层只追加。
4. **适配器单一职责**：跨厂商差异全部收敛在 `internal/adapter`，上层不得出现厂商专属逻辑（不得 import claude/codex SDK 到其他包）。
5. **internal 封闭**：`internal/` 下的包不得被本仓库之外引用；对外能力经 `pkg/` 与 API 暴露。
6. **零厂商锁定**：不依赖任何一家 agent 的私有 API；一切通过 PTY 输入输出 + 启发式状态识别 + 可选 ACP 协议。

## 3. 目录职责速览

| 目录 | 职责 |
|---|---|
| `cmd/drove` | CLI 主程序（bubbletea TUI）入口 |
| `cmd/droved` | 常驻 daemon 入口（可选运行模式） |
| `internal/agent` | Agent 抽象、状态机（唯一的状态权威） |
| `internal/auth` | 本地控制令牌生成、持久化与校验 |
| `internal/detect` | 每会话状态信号融合、去重与计时确认 |
| `internal/pty` | PTY 生命周期管理与字节流桥接 |
| `internal/term` | 流式终端控制序列清洗（不做屏幕仿真或查询应答） |
| `internal/event` | 事件模型、Hub 扇出、订阅 |
| `internal/session` | 会话编排：agent 创建、快照、回放 |
| `internal/store` | 持久化（SQLite，只追加事件日志） |
| `internal/adapter` | 跨厂商适配层（claude/codex/generic/ACP） |
| `internal/daemon` | daemon 生命周期、IPC |
| `internal/api` | REST + WebSocket API |
| `internal/config` | 配置加载与校验 |
| `internal/version` | 版本信息（由 ldflags 注入） |
| `pkg/` | 对外可复用公共包（当前留空） |
| `web/` | 前端（TS/React，规划中，MVP 未启用） |

## 4. 工程规范

- **语言**：Go 1.23+。并发一律 goroutine + channel；禁止裸 `sync.Mutex` 保护大段业务逻辑（用 channel 或局部临界区）。
- **错误处理**：错误必须 wrap（`fmt.Errorf("...: %w", err)`），禁止吞错；库代码返回 error，不 log.Fatal。
- **日志**：使用 `log/slog`；daemon 输出结构化日志，CLI 输出用户可读文本。
- **命名**：导出符号需注释；缩写遵循 Go 惯例（`ID`、`API`、`PTY`）。
- **测试**：核心状态机/事件/适配器必须有单测；新增行为默认配测试。
- **格式化**：`gofmt` 合规，`go vet` 零告警，CI 强制。

## 5. 常用命令

```bash
make build      # 编译 bin/drove 与 bin/droved
make test       # go test -race + 覆盖率摘要
make vet        # go vet
make lint       # gofmt + vet
```

## 6. 协作约定

- 改动必须保持"事件驱动 + 适配器隔离"两条主线。
- 新厂商适配：只允许在 `internal/adapter` 新增文件，并补 AGENTS.md 中该厂商条目。
- 禁止在 `internal` 包之间制造循环依赖；必要时用接口下沉到 `internal/agent` 或 `pkg/`。
- 提交信息遵循 Conventional Commits：`feat:` / `fix:` / `refactor:` / `docs:` / `test:`。
