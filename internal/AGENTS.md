# AGENTS.md — internal

## 职责

**内部实现包目录**（Go internal 规则：本仓库之外不可引用）。这是 Drove 的全部核心逻辑所在。

## 包与依赖方向（禁止循环依赖）

```
cmd/* ──▶ internal/client ──▶ internal/session
                                  │
    ┌──────────┬────────┬────────┼────────┬─────────┬─────────┐
    ▼          ▼        ▼        ▼        ▼         ▼
  agent      event    store     pty    adapter    detect
 (状态机)  (事件Hub) (SQLite) (PTY管理) (跨厂商) (信号融合)
```

- `session` 编排一切：agent + pty + adapter + detect + event + store。
- `daemon` 装配 session/api/config/store/hub（composition root）。
- `api` 只依赖 `session` 与 `event` 的公开接口。
- `client` 只做 JSON 透传（HTTP 客户端），不解析领域类型。
- `agent / event / store / pty / adapter / config / version` 之间无相互依赖（adapter 依赖 agent 的 State 类型除外）。

## 约束

- 新增包必须先写 `AGENTS.md`（职责、关键设计、约束三节）。
- 包级边界由上面的依赖方向约束；违反即视为架构违规，需评审。
- 禁止跨包访问未导出的符号；禁止用 `//go:linkname` 之类的绕行手段。
