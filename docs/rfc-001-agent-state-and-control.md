# RFC-001：Agent 状态识别与交互控制架构

- 状态：Draft
- 日期：2026-10-03
- 实现基线：`88d3148`
- 作者：DD（AI 助手起草，待 Duang777 评审）
- 相关 Issue：[#1](https://github.com/Duang777/drove/issues/1)（runner 交互模式）、[#2](https://github.com/Duang777/drove/issues/2)（hooks 状态权威）、[#3](https://github.com/Duang777/drove/issues/3)（Blocked 恢复）、[#4](https://github.com/Duang777/drove/issues/4)（输入注入）
- 调研依据：[Phase 1 hooks 与 Detector 资料调研](next-phase-research.md)

## 1. 背景与动机

Drove 的定位是"跨厂商 Agent 指挥台"：同时运行、观察、回放多个 AI coding agent，并实时识别每个 agent 的状态（Working / Blocked / Done / Idle），最终让人能"把它们往对的方向赶"。

当前实现有三块基础能力：状态机（transition 表）、事件 Hub、SQLite 事件溯源。Phase 0 和输入链路已经补齐了部分控制能力，但状态识别仍不可靠：

1. 状态识别 = 英文子串匹配（`Classify`），`Error` 出现在普通输出里就误判 Blocked；进了 Blocked 永远出不来；Confidence 被忽略。
2. runner 已支持 `interactive` 和 `oneshot`，新会话默认使用交互模式。
3. REST 和 CLI 输入已经接通，WebSocket 输入尚未定义双向消息协议。

本 RFC 给出 #1 至 #4 的统一方向。后续实现继续按可独立验证的阶段交付。

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

- 官方文档在 2026-10-03 列出 33 个事件。Phase 1A 只订阅根会话状态需要的最小集合，不复制完整列表。
- Claude Code 支持 `command`、`http`、`mcp_tool`、`prompt` 和 `agent` 五类 handler。Phase 1A 只使用两家都支持的 `command`。
- user、project、local、managed、plugin、skill 和 subagent 都可以提供 hooks。不同 settings 层的 hooks 会合并，不是简单覆盖。
- hook 以子进程运行，stdin 收到 JSON：`session_id`、`prompt_id`、`transcript_path`、`cwd`、`permission_mode`、`hook_event_name` 等；退出码 0 = 放行、2 = 阻断。
- 所有 matching hooks 并行运行。Drove 收到 `Stop` 时，另一个 hook 仍可能阻止该次 Stop，因此 `Stop` 只能先生成可取消的 Idle 候选。
- workspace trust 和 `allowManagedHooksOnly` 都可能让已配置的 hook 不执行。Drove 必须以收到当前会话的合法 signal 作为 hook 已激活的证据。
- `Notification` 不能统一映射为 Blocked。只有 `permission_prompt`、`elicitation_dialog`、`elicitation_url_dialog`、`agent_needs_input` 和 `quota_auto_resume_stale` 等明确等待人工的类型才是 Blocked 候选；`idle_prompt` 是 Idle 候选。

### 3.3 Codex hooks：已对齐 Claude 模型

- OpenAI 官方源码快照 `44dd77b` 定义 12 个事件：`PreToolUse`、`PermissionRequest`、`PostToolUse`、`PreCompact`、`PostCompact`、`SessionStart`、`SessionEnd`、`UserPromptSubmit`、`SubagentStart`、`SubagentStop`、`Stop` 和 `Interrupt`。
- Codex 解析 `command`、`mcp_tool`、`prompt` 和 `agent`。当前官方文档与源码都支持执行 `command` 和 `mcp_tool`，并跳过 `prompt` 和 `agent`。
- 配置来自活动配置层旁的 `hooks.json` 或 `config.toml` 内联 `[hooks]`。常用位置包括全局 `~/.codex/` 和项目 `<repo>/.codex/`。
- hooks 默认启用。正式 feature key 是 `hooks`，`codex_hooks` 只作为弃用别名保留。
- 项目配置受 project trust 约束。非 managed hook 还要按定义 hash 单独确认；定义变化后会停止执行，直到用户重新确认。
- Drove 不得自动使用 `--dangerously-bypass-hook-trust`，也不能把没有 signal 解释为 Idle。

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
L0  进程事实                 （终态权威：用户停止、daemon 关闭、进程退出）
L1  已确认激活的 hooks       （turn 与人工等待权威）
L2  启发式 Classify          （fallback，需去抖 + 置信度阈值）
L3  超时推断                 （最低优先级，仅生成 Idle 候选）
```

原则：

- 进程事实决定终态。hooks 不得把仍存活的 interactive 进程标记为 Done 或 Stopped。
- hook 必须由当前会话收到合法 signal 后才算激活。配置存在不代表 hook 可执行。
- hook 激活后，启发式只保留证据，不再写状态。hook 不可用时，启发式信号必须经过去抖并达到置信度阈值。
- 同一会话只有一个 Detector 提出状态迁移。hooks、PTY 和 timer 回调都只向 Detector 投递信号。
- 全局事件日志另有一个提交器，统一分配序号、落库和发布，避免不同会话写出乱序事件。

### 4.3 Detector：信号融合

新增 `internal/detect` 包：

```go
type Signal struct {
    Version         int
    Vendor          string
    VendorEvent     string
    Scope           string    // "root" | "subagent"
    VendorSessionID string
    VendorTurnID    string
    Notification    string
    Confidence      float64   // 0..1
    OccurredAt      time.Time // 仅作证据
    ReceivedAt      time.Time // 排序依据
    DeliveryID      string    // 幂等键
}
```

- Detector 维护每个 session 的信号流，按分层规则输出状态迁移建议；
- 状态机（`internal/agent`）保持为唯一的状态权威，只接受 Detector 的迁移指令；
- adapter 负责把厂商 JSON 压缩成 `Signal`，Detector 不解析 Claude 或 Codex 字段；
- prompt、tool input、transcript、assistant message 和原始厂商 JSON 不得进入事件日志；
- signal 和派生的状态事件必须在同一 SQLite batch 中提交。提交成功后才能更新内存状态并按序发布到 Hub；
- 状态页只展示脱敏后的 source、event、confidence 和枚举证据。

状态语义（精确定义）：

| 状态 | 定义 | 进入条件 |
|---|---|---|
| Working | 有活跃 turn | 根会话 UserPromptSubmit、工具活动或启发式高置信 |
| Blocked | 等待用户决策或输入 | PermissionRequest、人工等待 Notification、Elicitation 或启发式去抖 |
| Idle | 进程存活，无活跃 turn | Stop、Interrupt 或 idle_prompt 的确认窗口结束 |
| Done | oneshot 任务正常退出 | interactive 不自动进入 Done |
| Stopped | 进程已退出 | 进程退出事件 |

Blocked 恢复（对应 #3）：Blocked 后收到根会话 `UserPromptSubmit`、后续工具活动或已去抖的持续输出时，迁回 Working。

### 4.4 Hook 安装与会话关联

hook 收到的是厂商 session ID，不是 Drove agent ID。Drove 启动进程时注入：

```
DROVE_AGENT_ID=<drove agent id>
DROVE_SIGNAL_URL=http://127.0.0.1:<port>/api/v1/agents/<id>/signal
DROVE_SIGNAL_TOKEN=<per-session random token>
DROVE_SIGNAL_VENDOR=<claude|codex>
```

- `drove hook --vendor <vendor>` 从 stdin 读取厂商 JSON，附上环境变量中的关联信息和 `delivery_id`，再 POST 到 daemon。
- Phase 1A 只提供手工配置样例或隔离测试配置，不修改用户或项目配置。
- hook 策略分为 `off`、`auto` 和 `required`。`auto` 允许在 hook 不可用时降级，`required` 在未观察到合法 signal 时明确报错。
- Phase 1B 再提供显式的 `drove hooks install` 与 `uninstall`。安装器必须结构化合并 JSON 或 TOML、原子写入、记录所有权，并且只删除 Drove 拥有的节点。
- Drove 不代替用户接受 workspace、project 或 hook trust。

### 4.5 输入注入（对应 #4）

- REST `POST /api/v1/agents/{id}/input` 和 CLI `drove send` 已在 `88d3148` 完成。
- `agent.input` 只记录版本和字节数，不保存输入正文。
- 输入本身不改变状态。Phase 1 的 `UserPromptSubmit` signal 负责把 Blocked 或 Idle 恢复为 Working。
- WebSocket 双向输入使用 v1 input、ack 和 error 消息。每个连接由一个 writer
  goroutine 发送事件和响应，并拒绝重复 request ID。
- `drove attach <id>` 的全交互接管另开 RFC 或 issue。

### 4.6 事件模型扩展

事件类型都进入 SQLite 并支持回放：

- `agent.input` 已实现，payload 只包含版本和字节数。
- `agent.signal` 已实现，只保存 adapter 白名单中的枚举、ID、时间和脱敏证据，
  不保存厂商原始 JSON。
- 触发迁移的 `agent.signal` 与 `state_changed` 在同一批次连续提交。信号事件
  保存 `source`、`vendor_event`、`confidence` 和脱敏证据。

所有事件生产者必须经过一个全局提交器。提交器按接收顺序分配序号，使用
`Store.AppendEvents` 写入连续 batch，提交成功后更新内存投影，最后按序发布到
Hub。持久化失败时不得更新内存或发布未持久化事件。

## 5. 分阶段实施

- **Phase 0，已完成**（#1）：runner `mode` 字段、`--oneshot` 和 interactive 默认值。
- **Phase 1A，已完成**（#2、#3）：事件 reader-first 兼容、全局提交器、
  `internal/detect`、signal endpoint、`drove hook`、adapter 规范化和 Blocked
  恢复。
- **Phase 1B，待实施**：显式 hook 安装、幂等更新、精确卸载和 trust 状态展示。
- **Phase 2，已完成**（#4）：REST、CLI 和 WebSocket 输入已完成。
- **Phase 3，已完成**：ANSI 分类视图清洗（#5）和 daemon 会话投影恢复（#6）
  已完成。

## 6. 安全考虑

- `/signal` 回调端点只接受 loopback，并校验 agent ID 和每会话随机 token。hook 通过继承的环境变量取得这些值。
- token 只防止会话误串和偶然调用，不抵御 agent 本身或同一 OS 用户下的恶意进程。
- signal endpoint 限制 body 大小，严格解析 JSON，并拒绝无效 UTF-8、未知字段、错误 vendor、已 detach 会话和过期 token。
- input 注入在开放远程访问前必须加认证；本 RFC 范围内 daemon 仍只绑 `127.0.0.1`。
- hook 安装由显式命令触发，不能静默覆盖用户配置或绕过 Claude、Codex 的 trust 流程。
- signal 持久化必须脱敏，不保存 prompt、tool input、transcript 或 assistant message。

## 7. 已定参数与剩余限制

1. `Stop`、`StopFailure`、`Interrupt` 和 `idle_prompt` 使用 250 毫秒确认窗口。
   后续 hook 活动会取消 Idle 候选。
2. 无 hook 时，输出静默不会生成 Idle。Blocked 状态在 2 秒内收到两行普通输出
   后恢复为 Working。
3. `auto` 在厂商事件不受支持时保留启发式行为。`required` 在启动后 2 秒内没有
   收到合法 hook 时停止会话。
4. `creack/pty` 不支持 Windows。本 RFC 的 interactive 模式只支持 Unix。

## 附录 A：hook 事件 → drove 状态映射（初版）

| Hook 事件 | 信号等级 | 目标状态 | 备注 |
|---|---|---|---|
| SessionStart | L1 | 不迁移 | 证明当前会话的 hook 已激活 |
| UserPromptSubmit | L1 | Working | 新 turn 开始，也可解除 Blocked |
| PreToolUse、PostToolUse | L1 | Working | 根会话活跃，也可解除 Blocked |
| PermissionRequest | L1 | Blocked 候选 | 只在确实等待人工时采用 |
| 人工等待 Notification | L1 | Blocked 候选 | 只接受明确的 matcher allowlist |
| idle_prompt | L1 | Idle 候选 | 不是 Blocked |
| Stop | L1 | Idle 候选 | 等待确认窗口，后续活动可取消 |
| Interrupt | L1 | Idle 候选 | Codex turn 中断，不是进程退出 |
| SessionEnd | L1 | 不迁移 | 记录 signal，终态由进程事实决定 |
| SubagentStart、SubagentStop | L1 | 根会话保持 Working | 子代理停止不代表根会话 Idle |
| 进程正常退出 | L0 | Done 或 Stopped | oneshot 为 Done，interactive 为 Stopped |
| 用户停止、daemon 关闭、异常退出 | L0 | Stopped | 终态权威 |
