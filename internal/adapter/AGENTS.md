# AGENTS.md — internal/adapter

## 职责

**跨厂商适配层**。这是唯一允许出现厂商专属逻辑（命令名、启动参数、状态启发式）的包。上层（session/daemon/api）只与 `Runner` / `Heuristic` 接口打交道。

## 关键设计

- `Runner` 接口：`Command() (name, args, env)` —— 描述如何拉起某厂商的 CLI agent。
- `Heuristic` 接口：`Classify(line string) (StateHint, bool)` —— 从输出行推断状态信号（如 "Waiting for your input" → Blocked 提示）。
- `Registry` 按厂商标识注册实现；`For(vendor)` 返回实现，未知厂商回退 `generic`（即用户命令直接跑在 PTY 里，无启发式）。
- 内置厂商：`claude`（claude CLI）、`codex`（codex CLI）、`generic`。ACP 厂商作为预留条目（`acp` 尚未启用）。

## 约束

- 禁止在适配器之外引用厂商名做分支判断；新厂商只在本包加文件与注册。
- 启发式规则必须可测：每个模式有单测（`claude_test.go` 等），命中/误报都要覆盖。
- 状态判断只输出"提示"（hint），最终迁移决定权在 session 层（结合超时、进程退出等信号）。
- 导出类型：`Registry`、`Runner`、`Heuristic`、`StateHint`。
