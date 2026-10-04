# RFC-001：Agent 状态识别与交互控制架构

- 状态：已采纳，Phase 4 已完成；持久安装器延期
- 日期：2026-10-04
- Phase 1A 实现基线：`2717927`
- Phase 1B 实现基线：`48d68bd`
- 原始输出实现基线：`0c28281`
- 终端屏幕实现基线：`b202e70`
- 状态解释实现基线：`3290778`
- 终端验收基线：`a47daaf`
- 作者：DD
- 相关 Issue：[#1](https://github.com/Duang777/drove/issues/1)（runner 交互模式）、[#2](https://github.com/Duang777/drove/issues/2)（hooks 状态权威）、[#3](https://github.com/Duang777/drove/issues/3)（Blocked 恢复）、[#4](https://github.com/Duang777/drove/issues/4)（输入注入）、[#13](https://github.com/Duang777/drove/issues/13)（原始输出块与保留）、[#14](https://github.com/Duang777/drove/issues/14)（终端屏幕模型）、[#15](https://github.com/Duang777/drove/issues/15)（会话信号注入）
- 调研依据：[Phase 1 hooks 与 Detector 资料调研](next-phase-research.md)

## 1. 背景与动机

Drove 的定位是"跨厂商 Agent 指挥台"：同时运行、观察、回放多个 AI coding agent，并实时识别每个 agent 的状态（Working / Blocked / Done / Idle），最终让人能"把它们往对的方向赶"。

初始实现只有状态机、事件 Hub 和 SQLite 事件溯源。Phase 0、Phase 1A 和输入
链路现已落地：

1. runner 支持 `interactive` 和 `oneshot`，新会话默认使用交互模式。
2. 每个运行中会话由 Detector 融合 hook、进程、启发式和 timer 信号。
3. 全局提交器按一个顺序写入 SQLite、更新投影并发布到 Hub。
4. REST、CLI 和 WebSocket 输入均已接通。
5. Claude command hooks 和 Codex notify 默认按会话注入，不修改厂商持久配置。
6. PTY 输出按原始字节块提交，并从可过期附件回放。
7. terminal actor 应答终端查询、维护私有屏幕，并把稳定屏幕边沿交给 Detector。

本 RFC 记录 Issues #1 至 #4、#13 至 #15 的统一设计和当前实现。

## 2. 目标 / 非目标

目标：

- G1：agent 默认以交互模式常驻运行，`drove up` 之后是一个活的、可反复交互的会话。
- G2：状态识别从"猜输出"升级为"信号融合"。原生 hooks 是 turn 与人工等待的权威信号。Codex notify 只确认 Idle，启发式作为 fallback。每次状态变更记录来源、证据和置信度。
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

### 3.3 Codex hooks 与 notify

- OpenAI 官方源码快照 `44dd77b` 定义 12 个事件：`PreToolUse`、`PermissionRequest`、`PostToolUse`、`PreCompact`、`PostCompact`、`SessionStart`、`SessionEnd`、`UserPromptSubmit`、`SubagentStart`、`SubagentStop`、`Stop` 和 `Interrupt`。
- Codex 解析 `command`、`mcp_tool`、`prompt` 和 `agent`。当前官方文档与源码都支持执行 `command` 和 `mcp_tool`，并跳过 `prompt` 和 `agent`。
- 配置来自活动配置层旁的 `hooks.json` 或 `config.toml` 内联 `[hooks]`。常用位置包括全局 `~/.codex/` 和项目 `<repo>/.codex/`。
- hooks 默认启用。正式 feature key 是 `hooks`，`codex_hooks` 只作为弃用别名保留。
- 项目配置受 project trust 约束。非 managed hook 还要按定义 hash 单独确认；定义变化后会停止执行，直到用户重新确认。
- Drove 不得自动使用 `--dangerously-bypass-hook-trust`，也不能把没有 signal 解释为 Idle。
- Codex 顶层 `notify` 接受一个命令参数数组。Codex 在 turn 结束后把
  `agent-turn-complete` JSON 作为最后一个参数传给命令。
- notify 不经过原生 hook trust，但只报告 turn 结束。Drove 只在 fallback
  状态下用它生成 Idle 候选。notify 不会激活 hook 权威，也不能推断 Working
  或 Blocked。

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
L2  Codex notify             （仅在 fallback 中生成 Idle 候选）
L3  启发式 Classify          （fallback，需去抖 + 置信度阈值）
L4  超时推断                 （最低优先级，仅生成 Idle 候选）
```

原则：

- 进程事实决定终态。hooks 不得把仍存活的 interactive 进程标记为 Done 或 Stopped。
- hook 必须由当前会话收到合法 signal 后才算激活。配置存在不代表 hook 可执行。
- hook 激活后，Detector 仍持久化屏幕边沿，但只允许两个 Spec 006 例外改变状态。
  审批框消失可以把 Blocked 改为 Working。Claude 中断结果可以把 Working 改为
  Idle。Detector 把其他屏幕边沿标记为 `suppressed`。
- hook 不可用时，屏幕信号必须经过去抖并达到置信度阈值。
- notify 不改变 hook 状态。`awaiting_hook`、`hook_active` 和 `required` 会话只
  记录 notify，不让它驱动状态迁移。
- 同一会话只有一个 Detector 提出状态迁移。hooks、PTY 和 timer 回调都只向 Detector 投递信号。
- 全局事件日志另有一个提交器，统一分配序号、落库和发布，避免不同会话写出乱序事件。

### 4.3 Detector：信号融合

新增 `internal/detect` 包：

```go
type Signal struct {
    Version         int
    Source          string
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

Codex notify 只允许根会话的 `turn_stopped`。在 fallback 中，经过 1 秒确认后，
它可以把 Working 或 Blocked 改为 Idle。后续输出、原生 hook 或更新的 notify
会取消或替换候选。

### 4.4 会话信号注入与关联

厂商信号包含厂商 session ID，不包含 Drove agent ID。Drove 启动进程时注入：

```
DROVE_AGENT_ID=<drove agent id>
DROVE_SIGNAL_URL=http://127.0.0.1:<port>/api/v1/agents/<id>/signal
DROVE_SIGNAL_TOKEN=<per-session random token>
```

- `drove hook --vendor <vendor>` 从 stdin 读取厂商 JSON。Codex notify 使用
  `--payload-argv` 读取唯一的位置参数。两种方式都附上环境变量中的关联信息和
  `delivery_id`，再 POST 到 daemon。
- Claude 默认生成只含 hooks 的会话专用 settings 文件，并通过 `--settings`
  加载。Codex 默认通过 `-c notify=[...]` 注入 relay 命令。两种方式都不修改
  用户或项目配置。
- `agents.<vendor>.signal_injection` 接受 `auto` 和 `off`。调用方已经传入
  Claude settings 参数、`--bare` 或 Codex notify 时，Drove 保留调用方设置并
  跳过注入。
- hook 策略分为 `off`、`auto` 和 `required`。`auto` 允许在原生 hook 不可用时
  降级。`required` 只接受原生 hook，Codex notify 不能满足该策略。
- [手工配置指南](hooks.md)仍支持原生 hooks。持久安装器是延期的可选能力，
  只用于让 Drove 之外启动的厂商进程也上报状态。
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

### 4.7 原始输出与保留（对应 #13）

- PTY 使用 32 KiB 缓冲区立即交付带源 offset 的字节块，不再等待换行；正常块
  不拆分有效 UTF-8，EOF 仍保留无效或不完整尾部字节。
- 新 writer 只产生 `output.chunk`。事件 envelope 保存版本、offset 和长度，
  原始 BLOB 保存在 `output_chunks` 附件表；旧 `output` 行事件保持可读。
- signal token 在流式处理器中跨块等长脱敏。Store 成功后，输出才进入 Hub 和
  Detector 的派生文本路径。
- `drove log` 默认回放原始终端字节，`--plain` 使用共享流式清洗器移除控制序列。
- `storage.output_retention_days` 默认 30，`0` 表示永久保留。清理只删除到期
  附件，所有 event envelope、投影事实和全局序号保持不变。
- 清理启用 SQLite `secure_delete` 并执行 WAL truncate checkpoint，不执行
  `VACUUM`，因此数据库文件已分配的大小可能不变。
- `d11f6c3` 是 reader-first 回滚下限。一旦 writer 写入 `output.chunk`，不能
  回滚到不识别该事件的更早版本。

### 4.8 终端屏幕、查询应答与状态解释（对应 #14）

每个 attached 会话持有一个 terminal actor。该 actor 独占 x/vt controller、
厂商 screen classifier、容量 64 的 inbox、100 ms 固定采样计时器和当前不可变
snapshot。observation actor 继续独占 Detector 和状态确认 timer。终端解析不会
阻塞 hook 或进程事实。

输出处理顺序固定为：

1. 全局 Committer 把 `output.chunk` 写入 SQLite。
2. Committer 更新投影并发布到 Hub。
3. 输出处理器用提交 receipt 构造 `term.CommittedChunk`。
4. terminal actor 按 offset 顺序把已提交字节写入 x/vt。
5. 首个 dirty chunk 启动一个不滑动的 100 ms 采样窗口。
6. classifier 只在规则 presence 变化时向 observation actor 发送边沿。

x/vt reply pump 在第一次 controller write 前就绪。DSR、OSC 10/11、DA1 和
Kitty keyboard reply 以完整帧直接调用 `pty.Session.Write`。reply 与用户输入
共用 PTY 写锁，但不经过 `Manager.SendInput`，不产生 `agent.input`，也不进入
输出、Hub、回放、snapshot 或 explain。子进程显式回显 reply 时，回显属于新的
PTY 输出。

screen signal 和 state evidence 使用 version 3 payload。持久字段只有稳定规则
名、`present` 或 `cleared` 边沿、区域、静态 evidence、已提交 output offset 和
最终 output sequence。私有 matcher、屏幕文字和屏幕 hash 不进入事件日志。
Detector 持久化 `candidate`、`suppressed`、`transitioned`、`stale` 和
`terminal` 结果，因此 `drove explain` 能说明状态为什么变化或没有变化。

`GET /api/v1/agents/{id}/explain` 和 `drove explain <id>` 默认返回最近 50 条
signal 和 state 事件，`--limit` 的上限是 200，`--json` 返回类型化响应。只有
同一个仍 attached 的 terminal actor 可以提供 live snapshot。snapshot 最多取
底部 12 行，每行最多 160 个 cells，编码后最多 4 KiB。detach 后只返回持久化
决策。

snapshot 使用已经完成 signal token 等长打码的 committed output，但没有通用
密钥扫描。调用方必须把它视为可能包含源码、prompt 或凭据的临时数据。原始输出
保留策略不变。

实现固定 `github.com/charmbracelet/x/vt` 于
`v0.0.0-20261004011457-ad85c59fdf4e`，并把源码构建下限提高到 Go 1.24.2。
CI 在 Ubuntu 上分别运行精确的 Go 1.24.2 和当前 stable。数据库采用
reader-first 发布：`f361ab5` 先加入 version 3 reader，`b202e70` 后启用 writer。
数据库写入 version 3 后，`f361ab5` 是最低回滚版本。

32 个 terminal actor 的实测平均耗时是 1.026781392 s/op，聚合吞吐是
31.19 MiB/s。每轮每个 actor 接收 1 MiB committed bytes，峰值 goroutine 为
99，最大 inbox 深度为 1，未触发 backpressure。完整硬件、分配和 race 数据见
[技术笔记](technical-notes.md#9-terminal-actor-32-session-benchmark)。

public resize、WebSocket 终端流和 attach 不在本阶段。终端流由
[#19](https://github.com/Duang777/drove/issues/19) 跟踪，Web attach 与终端 UI
由 [#20](https://github.com/Duang777/drove/issues/20) 跟踪。可选持久 hook
安装器仍由 [#24](https://github.com/Duang777/drove/issues/24) 跟踪。

## 5. 分阶段实施

- **Phase 0，已完成**（#1）：runner `mode` 字段、`--oneshot` 和 interactive 默认值。
- **Phase 1A，已完成**（#2、#3）：事件 reader-first 兼容、全局提交器、
  `internal/detect`、signal endpoint、`drove hook`、adapter 规范化和 Blocked
  恢复。
- **Phase 1B，已完成**（#15）：Claude `--settings`、Codex `notify`、relay
  argv payload、注入状态元数据、私有临时文件和清理。
- **可选持久安装，延期**：显式安装、幂等更新、精确卸载和 trust 状态展示。
  设计输入见 [hook 管理调研](phase-1b-hook-management-research.md)。
- **Phase 2，已完成**（#4）：REST、CLI 和 WebSocket 输入已完成。
- **Phase 3，已完成**：ANSI 分类视图清洗（#5）和 daemon 会话投影恢复（#6）
  已完成。
- **Phase 4，已完成**：原始 PTY 字节块、回放和输出保留（#13），以及终端
  仿真、查询应答、屏幕规则和状态解释（#14）均已完成。

## 6. 安全考虑

- `/signal` 回调端点只接受 loopback，并校验 agent ID 和每会话随机 token。hook 通过继承的环境变量取得这些值。
- token 只防止会话误串和偶然调用，不抵御 agent 本身或同一 OS 用户下的恶意进程。
- signal endpoint 限制 body 大小，严格解析 JSON，并拒绝无效 UTF-8、未知字段、错误 vendor、已 detach 会话和过期 token。
- input 注入使用控制面 bearer token；daemon 仍只绑定 loopback。
- 默认会话注入不写厂商持久配置。延期的持久安装必须由显式命令触发，不能
  静默覆盖用户配置或绕过 Claude、Codex 的 trust 流程。
- Claude 临时目录使用 `0700`，settings 文件使用 `0600`。token 只进入继承的
  进程环境，不进入参数、临时文件、事件或日志。
- signal 持久化必须脱敏，不保存 prompt、tool input、transcript 或 assistant message。
- 原始输出可能包含源码和凭据，因此默认只保留 30 天；附件到期后，事件
  metadata 仍用于审计和序号恢复。
- version 3 screen evidence 不保存屏幕文字或 hash。attached snapshot 没有
  通用密钥扫描，API 客户端不得持久化它。

## 7. 已定参数与剩余限制

1. `Stop`、`StopFailure`、`Interrupt` 和 `idle_prompt` 使用 1 秒确认窗口。
   后续 hook 活动会取消 Idle 候选。
2. 无 hook 时，输出静默 60 秒后可以生成 Idle。Blocked 状态在 1 秒内收到两行
   普通输出后恢复为 Working。启发式状态信号的最低置信度是 0.85。
3. `auto` 在厂商事件不受支持、relay 不可用或参数冲突时保留启发式行为。
   `required` 在启动后 5 秒内没有收到合法原生 hook 时停止会话。Codex notify
   只在 fallback 中确认 Idle。
4. `creack/pty` 不支持 Windows。本 RFC 的 interactive 模式只支持 Unix。
5. 当前只使用固定的 40x120 初始尺寸。public resize、WebSocket 终端流和
   attach 分别由 Issues #19 和 #20 跟踪。
6. live snapshot 只有底部受限视图，不包含 scrollback、标题、样式、链接或
   clipboard 数据，也不承诺通用密钥识别。

## 附录 A：hook 与 notify 状态映射

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
| Codex agent-turn-complete | L2 | Idle 候选 | 只在 fallback 中使用，不激活 hook 权威 |
| SessionEnd | L1 | 不迁移 | 记录 signal，终态由进程事实决定 |
| SubagentStart、SubagentStop | L1 | 根会话保持 Working | 子代理停止不代表根会话 Idle |
| 进程正常退出 | L0 | Done 或 Stopped | oneshot 为 Done，interactive 为 Stopped |
| 用户停止、daemon 关闭、异常退出 | L0 | Stopped | 终态权威 |
