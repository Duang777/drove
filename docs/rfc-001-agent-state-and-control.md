# RFC-001：Agent 状态识别与交互控制架构

- 状态：Draft
- 日期：2026-10-03
- 作者：DD（AI 助手起草，待 Duang777 评审）
- 相关 Issue：[#1](https://github.com/Duang777/drove/issues/1)（runner 交互模式）、[#2](https://github.com/Duang777/drove/issues/2)（hooks 状态权威）、[#3](https://github.com/Duang777/drove/issues/3)（Blocked 恢复）、[#4](https://github.com/Duang777/drove/issues/4)（输入注入）

## 1. 背景与动机

Drove 的定位是"跨厂商 Agent 指挥台"：同时运行、观察、回放多个 AI coding agent，并实时识别每个 agent 的状态（Working / Blocked / Done / Idle），最终让人能"把它们往对的方向赶"。

当前实现（v0.1）有三块是实的：状态机（transition 表）、事件 Hub、SQLite 事件溯源。但"识别"与"指挥"两块是虚的：

1. 状态识别 = 英文子串匹配（`Classify`），`Error` 出现在普通输出里就误判 Blocked；进了 Blocked 永远出不来；Confidence 被忽略。
2. runner 用的是 `claude --print` / `codex exec` 一次性模式，agent 跑一次就退出——状态机设计的 Working / Blocked / Idle 前提（agent 常驻）根本不存在。
3. `Manager.Write` 写了但没有任何入口调用——只能看，不能指挥。

本 RFC 提出一套统一架构，一次性解决 #1–#4。

## 2. 目标 / 非目标

目标：

- G1：agent 默认以交互模式常驻运行，`drove up` 之后是一个活的、可反复交互的会话。
- G2：状态识别从"猜输出"升级为"信号融合"：hooks 为权威信号，启发式降级为 fallback，每次状态变更可解释（来源 / 证据 / 置信度）。
- G3：打通输入注入：API + CLI + WebSocket 三路可向运行中的 agent 发送指令。
- G4：所有状态变更与信号进入事件日志，可回放、可审计。

非目标：

- NG1：本 RFC 不引入 ACP 作为传输（见 §3.4）。
- NG2：不做跨机器 / 多用户（daemon 仍只绑 localhost）。
- NG3：不做进程级别的断电恢复（#6 另行处理）。

## 3. 调研结论

### 3.1 herdr：两层模型是行业实践

herdr（Rust 单二进制，YC F26，对标项目）的状态检测分两层：

- 有 hook / plugin 时，hook 上报的生命周期事件是唯一权威（lifecycle authority）；
- 无 hook 时，fallback 到 screen manifest（抓取 pane 底部屏幕内容）做启发式；
- 关键原则：同一会话不同时运行两套相互竞争的状态权威。

Drove 照搬这套：hooks > 启发式，且同一时刻只有一个写入者（Detector）能驱动状态机。

### 3.2 Claude Code hooks：可直接用的权威信号

- 配置位置（按优先级）：`managed-settings.json` → `~/.claude/settings.json` → `.claude/settings.json` → `.claude/settings.local.json` → plugin `hooks/hooks.json`。
- 事件：`SessionStart`（matcher: startup / resume / clear / compact）、`SessionEnd`、`UserPromptSubmit`、`PreToolUse`（可阻断）、`PostToolUse`、`PermissionRequest`、`Notification`、`Stop`（可阻断）、`PreCompact` / `PostCompact`、`SubagentStart` / `SubagentStop`。
- hook 以子进程运行，stdin 收到 JSON：`session_id`、`prompt_id`、`transcript_path`、`cwd`、`permission_mode`、`hook_event_name` 等；退出码 0 = 放行、2 = 阻断。
- 对 drove 有用的映射：`UserPromptSubmit` → 新 turn 开始；`PreToolUse` / `PostToolUse` → 活跃心跳；`PermissionRequest` / `Notification` → 等待用户（Blocked）；`Stop` → turn 结束（Idle）；`SessionEnd` → 进程结束。

### 3.3 Codex hooks：已对齐 Claude 模型

- 11 个事件，与 Claude Code 基本对齐：`SessionStart`、`SessionEnd`、`UserPromptSubmit`、`PreToolUse`、`PostToolUse`、`PermissionRequest`、`PreCompact`、`PostCompact`、`SubagentStart`、`SubagentStop`、`Stop`。
- 配置：项目 `.codex/hooks.json` 或全局 `~/.codex/hooks.json`（`CODEX_HOME` 可覆盖）；同样是 matcher + JSON stdin 机制（codex-cli 0.149.0 已验证）。
- 注意：非托管 hook 在全新安装上首次运行需要用户信任确认——`drove up` 时要把这一步暴露给用户，不能静默失败。

### 3.4 ACP：方向正确，但现在不做

- ACP（JSON-RPC 2.0 over stdio）：`session/prompt` 返回 `stopReason`（`end_turn` / `cancelled` / `max_tokens` / `refusal`），`session/update` 流式推送 `agent_message_chunk` / `tool_call` / `tool_call_update` / `usage_update`；v2 RFD 甚至在讨论 `state_update`（running / idle / requires-action，正好对应 Working / Idle / Blocked）。
- 暂缓理由：ACP 要求 agent 以 stdio 子进程方式运行，与 Drove"agent 必须在真实 PTY 里跑"（可接管、可回放终端）的第一原则冲突；远程传输仍在演进。待 v2 `state_update` 稳定后再评估把 ACP 作为第三种信号源。

## 4. 总体设计

### 4.1 Runner：interactive 为默认，oneshot 为选项（对应 #1）

- `StartRequest` 增加 `mode: interactive | oneshot`（默认 interactive）；`drove up` 增加 `--oneshot` flag。
- interactive：`claude` / `codex`（不带 `--print` / `exec`），进程常驻 PTY。
- oneshot：保持现有行为，状态机简化为 Pending → Working → Done / Stopped。

### 4.2 信号分层（Signal hierarchy）

```
L0  hooks 生命周期事件      （权威，immediate）
L1  进程/协议事件            （权威，immediate：进程退出、PTY EOF）
L2  启发式 Classify         （fallback，需去抖 + 置信度阈值）
L3  超时推断                （最低优先级，仅用于 Idle 推断）
```

原则：

- L0 / L1 信号直接驱动状态机；L2 信号必须经过去抖（连续 N 次一致 hint 或 M 秒窗口）且 confidence ≥ 阈值；
- 同一会话的状态机只有一个写入者：Detector。hooks 回调、PTY 回调都只向 Detector 投递信号，不直接改状态。

### 4.3 Detector：信号融合

新增 `internal/detect` 包：

```go
type Signal struct {
    Source     string    // "hook" | "process" | "heuristic" | "timeout"
    Event      string    // "Stop" | "UserPromptSubmit" | ...
    Confidence float64   // 0..1
    Evidence   string    // 触发证据（hook payload 摘要 / 匹配到的行）
    At         time.Time
}
```

- Detector 维护每个 session 的信号流，按分层规则输出状态迁移建议；
- 状态机（`internal/agent`）保持为唯一的状态权威，只接受 Detector 的迁移指令；
- 每次迁移写入事件日志时携带 `source` / `evidence` / `confidence`，状态页可解释"为什么是 Blocked"。

状态语义（精确定义）：

| 状态 | 定义 | 进入条件 |
|---|---|---|
| Working | 有活跃 turn | UserPromptSubmit / PreToolUse / 启发式高置信 |
| Blocked | 等待用户决策 / 输入 | PermissionRequest / Notification / 启发式（去抖后） |
| Idle | 进程存活，无活跃 turn | Stop hook / turn 结束 |
| Done | 任务完成 | 见 §7 开放问题（默认：oneshot 正常退出；interactive 需显式信号） |
| Stopped | 进程已退出 | 进程退出事件 |

Blocked 恢复（对应 #3）：Blocked 后收到 Working 类信号（UserPromptSubmit、新一轮 PreToolUse、或持续新输出）→ 迁回 Working。

### 4.4 Hook 安装与会话关联

关键问题：hook 收到的是 agent 自己的 `session_id`，不是 drove 的 session id。解法是环境变量——drove spawn agent 进程时注入：

```
DROVE_SESSION_ID=<drove session id>
DROVE_CALLBACK_URL=http://127.0.0.1:<port>/api/v1/agents/<id>/signal
```

- drove 提供 `drove hook` 子命令：从 stdin 读 hook JSON，附上 env 里的会话信息，POST 到 daemon。hook 配置里只需写 `"command": "drove hook"`——单二进制，无外部脚本依赖。
- `drove up` 时把 drove 的 hooks **合并**写入用户 / 项目配置（claude：`~/.claude/settings.json` 或项目 `.claude/settings.json`；codex：`~/.codex/hooks.json`），用可识别的 command 标记以便清理；绝不覆盖用户已有配置。
- 会话结束时提供清理（`drove up --no-hooks` 可跳过安装）。

### 4.5 输入注入（对应 #4）

- API：`POST /api/v1/agents/{id}/input`，body `{"data": "..."}`，写入 PTY stdin；事件日志记 `agent.input`。
- CLI：`drove send <id> "prompt..."`，支持 `--stdin` 管道。
- WebSocket：双向消息 `{"type":"input","data":"..."}`。
- 语义：经 PTY stdin 注入的输入对 agent 而言就是用户输入，会正常触发 `UserPromptSubmit` hook → 状态机自动回到 Working，链路自洽。
- 后续：`drove attach <id>`（全交互接管）另开 RFC / issue。

### 4.6 事件模型扩展

新增事件类型（都进 SQLite，照常回放）：

- `agent.signal`：收到的原始信号（含 source / event / confidence / evidence）
- `agent.input`：注入的输入（可节选，避免日志爆炸）
- 状态迁移事件 payload 增加 `source`、`evidence`、`confidence` 字段。

## 5. 分阶段实施

- **Phase 0**（#1）：runner `mode` 字段 + `--oneshot`；interactive 为默认。验收：`drove up claude` 常驻。
- **Phase 1**（#2、#3）：`internal/detect` 包 + 信号分层 + `drove hook` 子命令 + `drove up` 自动安装 hooks（claude / codex）；Blocked 恢复逻辑。验收：Stop / UserPromptSubmit 驱动状态准确；误判可恢复。
- **Phase 2**（#4）：input API + `drove send` + WS 双向。验收：send 后 agent 响应且状态机回到 Working。
- **Phase 3**：ANSI 剥离（#5）、daemon 重启恢复（#6）。

## 6. 安全考虑

- `/signal` 回调端点只接受 localhost，且校验 `DROVE_SESSION_ID` 与 token（daemon 启动时生成的随机 token，经 env 传给 agent 进程，hook 继承）。
- input 注入在开放远程访问前必须加认证；本 RFC 范围内 daemon 仍只绑 `127.0.0.1`。
- hook 安装必须显式合并用户配置；Codex 的首次信任提示要透出给用户。

## 7. 开放问题

1. **Done 的权威判定**：turn 结束（Stop）≠ 任务完成。Phase 1 暂定：oneshot 正常退出 → Done；interactive 下 Done 需显式信号（`drove done <id>` 或未来 ACP `state_update`）。是否需要在 Stop hook 里做 transcript 语义判断？（倾向不做，保持 hook 轻量。）
2. **hook 配置的生命周期**：会话结束时是否移除 drove 安装的 hooks？倾向：默认保留（幂等合并），`drove hooks uninstall` 提供清理。
3. **无 hook 环境的 Idle 推断**：纯启发式下，输出静默多久算 Idle？阈值可配置，默认 5 分钟，且仅在无 L0 / L1 信号时生效。
4. **Windows PTY**：`creack/pty` 不支持 Windows；本 RFC 不覆盖，interactive 模式暂只支持 unix。

## 附录 A：hook 事件 → drove 状态映射（初版）

| Hook 事件 | 信号等级 | 目标状态 | 备注 |
|---|---|---|---|
| SessionStart | L0 | —（确认 Starting→Working） | 仅确认，不迁移 |
| UserPromptSubmit | L0 | Working | 新 turn 开始 |
| PreToolUse / PostToolUse | L0 | Working | 活跃心跳 |
| PermissionRequest | L0 | Blocked | 等待用户决策 |
| Notification | L0 | Blocked | 需要用户注意 |
| Stop | L0 | Idle | turn 结束 |
| SessionEnd | L1 | Stopped | 进程结束 |
| SubagentStart / Stop | L0 | Working | 视为活跃 |
