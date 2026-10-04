# Phase 1 hooks 与 Detector 资料调研

- 调研日期：2026-10-03
- Drove 实现基线：[`88d3148`](https://github.com/Duang777/drove/commit/88d3148b0f9caf52ddef327f56872c1384030ad2)
- Phase 1A 交付基线：[`2717927`](https://github.com/Duang777/drove/commit/271792720012b072a087e53839823898800fe05d)
- Phase 1B 会话注入基线：[`48d68bd`](https://github.com/Duang777/drove/commit/48d68bdb9dd6bdf761983d4e3724209010d0ab60)
- 原始输出与保留基线：[`0c28281`](https://github.com/Duang777/drove/commit/0c282811c5f4b6271624c43a2f268cc2b8c53480)
- 终端屏幕实现基线：[`b202e70`](https://github.com/Duang777/drove/commit/b202e70)
- 状态解释实现基线：[`3290778`](https://github.com/Duang777/drove/commit/3290778)
- 终端验收基线：[`a47daaf`](https://github.com/Duang777/drove/commit/a47daaf)
- Claude Code 文档读取日期：2026-10-03
- Codex 源码快照：[`44dd77b`](https://github.com/openai/codex/commit/44dd77b71e88c78295736bffd3dc3b684c13be6d)
- 目标范围：RFC-001 Phase 1，关联 Issue [#2](https://github.com/Duang777/drove/issues/2)、[#3](https://github.com/Duang777/drove/issues/3) 与 [#15](https://github.com/Duang777/drove/issues/15)
- 可选持久安装调研：[hook 管理调研](phase-1b-hook-management-research.md)

## 结论

本次更新前的 RFC-001 草案不能直接用于 Phase 1 编码。旧稿中的 Claude Code
事件列表已经落后，Codex 事件数、handler 支持情况和信任模型也不准确。本次
RFC 更新已校正这些事实。两家当前都能稳定提供 `command` hook，因此 Phase 1A
应以“厂商 command hook → `drove hook` relay → 本地 signal endpoint →
单写者 Detector”为最小公共链路。

Phase 1A 编码前必须固定五项协议：

1. 状态的精确定义和信号优先级，尤其是 `Stop`、`Notification`、子代理事件、
   `Done` 与进程退出的关系。
2. 每会话 Detector 与全局事件提交器的边界，以及内存状态、SQLite 和 Hub 的
   可见顺序。
3. 事件格式升级后的回滚下限，避免旧二进制因未知事件无法启动。
4. hook 激活、降级和配置所有权，不在第一阶段自动改写用户或项目配置。
5. vendor adapter、通用 Detector、HTTP 边界和 session 编排之间的职责。

这些协议定案后，Phase 1A 才是可实现状态。Issue #15 后续选择按会话注入作为
默认路径。持久安装与精确卸载仍是可选能力，不与 Detector 核心路径绑定。

## 当前实现基线

本调研开始时的基线是 `88d3148`。当前实现已完成本调研提出的 Phase 1A，
并补齐 WebSocket 输入：

| 范围 | 当前状态 | 一手证据 |
| --- | --- | --- |
| REST 输入 | 已完成 | `internal/api/server.go` 与 `internal/session/session.go` 实现输入校验、PTY 写入和脱敏审计。 |
| Session 输入与审计 | 已完成 | `agent.input` 只保存版本和字节数，恢复投影将它视为 projection-neutral。 |
| CLI 输入 | 已完成 | `drove send <id> [text]` 与 `--stdin` 均已实现。 |
| WebSocket 输入 | 已完成 | v1 input、ack 和 error 消息已实现，连接内 request ID 去重上限为 4096。 |
| hooks / Detector | 已完成 | 每个 live session 持有一个 Detector。hook 权威、启发式 fallback、置信度、去重、Blocked 恢复和 Idle 确认均有 race 测试。 |
| 会话信号注入 | 已完成 | Claude 使用临时 `--settings`，Codex 使用进程级 `notify`。两种方式都不修改厂商持久配置。 |
| 原始 PTY 输出 | 已完成 | `output.chunk` 保存带 offset 的原始字节附件；CLI 支持 raw 和 plain 回放，附件默认保留 30 天。 |
| 终端屏幕检测 | 已完成 | terminal actor 只读取 committed output，x/vt 应答启动查询，adapter 产生稳定屏幕边沿，Detector 决定状态权威。 |
| 状态解释 | 已完成 | `drove explain` 和 explain API 返回受限决策尾部；只有 attached 会话返回临时受限屏幕。 |

Issue [#2](https://github.com/Duang777/drove/issues/2)、
[#3](https://github.com/Duang777/drove/issues/3) 和
[#4](https://github.com/Duang777/drove/issues/4) 的实现条件已经满足。
Issue [#14](https://github.com/Duang777/drove/issues/14) 的终端查询、屏幕检测、
状态解释和验收条件也已经满足。

## Issue #15 的会话注入决策

Phase 1A 只提供 relay、signal endpoint 和手工 hook 配置。Issue #15 在该边界
之上增加进程级配置：

- Claude adapter 生成只含 17 组 hooks 的 settings 文件。session 层以 `0600`
  权限写入该文件，并通过 `--settings` 加载。
- Codex adapter 通过顶层 `-c notify=[...]` 传入 relay argv。Codex 将
  `agent-turn-complete` JSON 作为最后一个参数传给 relay。
- `internal/adapter` 只规划厂商参数和临时文件。`internal/session` 负责私有
  目录、原子写入、进程生命周期和清理。
- `signal_injection` 与 hook 策略分开。`auto` 尝试注入，`off` 保留原命令。
  原生 hook 决定 `hook_active`，注入成功本身不代表 hook 已运行。

Codex notify 不是原生 hook。它只在 Detector 已进入 fallback 后生成一秒 Idle
候选，不能满足 `required`，也不能推断 Working、Blocked 或终态。原生 hook
激活后，Detector 只记录 notify，不让它驱动状态迁移。

Drove 不注入 Codex trust hash，不使用 trust bypass，也不写用户或项目配置。
后续实现用终端屏幕补齐 fallback 权威，但没有注入 Codex OSC 9。持久安装器仍由
[Issue #24](https://github.com/Duang777/drove/issues/24) 跟踪。

## Claude Code 的当前 hook 模型

以下结论来自 Claude Code 官方
[`Hooks reference`](https://code.claude.com/docs/en/hooks)，读取于
2026-10-03。该页面是动态文档，所以本文保留读取日期。

### 事件

官方 reference 当前列出 33 个事件，而不是 RFC-001 中的旧集合：

- 会话与初始化：`Setup`、`SessionStart`、`SessionEnd`。
- turn：`UserPromptSubmit`、`UserPromptExpansion`、`Stop`、`StopFailure`。
- 工具和权限：`PreToolUse`、`PermissionRequest`、`PermissionDenied`、
  `PostToolUse`、`PostToolUseFailure`、`PostToolBatch`。
- 子代理和任务：`SubagentStart`、`SubagentStop`、`TaskCreated`、
  `TaskCompleted`、`TeammateIdle`。
- 上下文与模型：`PreCompact`、`PostCompact`、`PreModelSwitch`、
  `PostModelSwitch`、`InstructionsLoaded`。
- MCP 交互：`Elicitation`、`ElicitationResult`。
- 工作区与界面：`Notification`、`MessageDisplay`、`ConfigChange`、
  `CwdChanged`、`DirectoryAdded`、`FileChanged`、`WorktreeCreate`、
  `WorktreeRemove`。

事件时机和 matcher 字段以官方
[`Hook lifecycle`](https://code.claude.com/docs/en/hooks#hook-lifecycle) 与
[`Matcher patterns`](https://code.claude.com/docs/en/hooks#matcher-patterns)
为准。Phase 1A 不需要订阅全部事件，只应选择能解释根会话状态的最小集合。

### Handler 类型

Claude Code 当前定义五类 handler：

| 类型 | 行为 | Phase 1A 结论 |
| --- | --- | --- |
| `command` | 在子进程 stdin 接收事件 JSON，以 stdout、stderr 和退出码返回结果 | 采用，作为两家厂商的共同基线 |
| `http` | 向 URL 发送同一份 JSON | 不采用；Codex 没有对应类型，且 Claude 还受 URL 与 header 环境变量 allowlist 约束 |
| `mcp_tool` | 调用已配置 MCP server 的 tool | 不采用；启动时可用性和厂商支持不一致 |
| `prompt` | 调用模型做单轮判断 | 不采用；状态上报不应引入模型成本与非确定性 |
| `agent` | 启动子代理做判断 | 不采用；该能力仍标为实验性 |

五类定义见官方
[`Hook handler fields`](https://code.claude.com/docs/en/hooks#hook-handler-fields)。
“全局存在五种类型”不表示每个事件都支持每种类型；事件章节仍是最终约束。

### 信任边界

Claude Code 的 hook 不是沙箱内的低权限回调：

- command hook 以当前用户的完整权限运行，并继承 Claude Code 的工作目录和
  大部分环境变量。官方安全说明要求把 hook command 当作可执行代码审查
  （[`Security considerations`](https://code.claude.com/docs/en/hooks#security-considerations)）。
- 交互会话在用户接受 workspace trust 前不会运行 settings 中的 hooks
  （[`Workspace trust`](https://code.claude.com/docs/en/hooks#workspace-trust)）。
- user、project、local、managed、plugin、skill 和 subagent 都可能提供 hooks；
  settings 层的 hook 会合并，而不是简单覆盖
  （[`Hook locations`](https://code.claude.com/docs/en/hooks#hook-locations)）。
- 管理员可用 `allowManagedHooksOnly` 禁止 user、project、local 和普通 plugin
  hooks（[`settings reference`](https://code.claude.com/docs/en/settings-reference#allowmanagedhooksonly)）。

这意味着“配置文件中存在 Drove hook”不等于“hook 已生效”。Detector 必须区分
`configured`、`trusted/allowed`、`observed active` 和 `unavailable`。

### `Notification` 不能统一映射为 Blocked

官方 matcher 列出的 notification 类型有不同语义。Phase 1A 只能把明确表示
等待人工的类型作为 Blocked 候选：

| Notification 类型 | 建议解释 |
| --- | --- |
| `permission_prompt` | Blocked 候选 |
| `elicitation_dialog`、`elicitation_url_dialog` | Blocked 候选 |
| `agent_needs_input` | Blocked 候选 |
| `quota_auto_resume_stale` | Blocked 候选，需要用户检查才能恢复 |
| `idle_prompt` | Idle 候选，不是 Blocked |
| `auth_success`、`elicitation_complete`、`elicitation_response` | 活动或恢复证据，不直接进入 Blocked |
| `agent_completed` | turn/任务提示，不等于 Drove 的终态 Done |
| `quota_auto_resume_fired`、`quota_auto_resume_disabled` | 记录为 signal，不直接改变状态 |

此外，独立的 `Elicitation` 与 `ElicitationResult` 已经提供更直接的等待与恢复
信号。Notification 类型以官方
[`Matcher patterns`](https://code.claude.com/docs/en/hooks#matcher-patterns)
表为准。

## OpenAI Codex 的当前 hook 模型

Codex 仍在快速演进。本文同时引用已发布官方文档与 2026-10-02 的官方仓库
快照；二者冲突时，Phase 1A 只依赖共同确认的能力，并执行版本与能力探测。

### 事件

官方源码在 `44dd77b` 明确定义 12 个事件：

`PreToolUse`、`PermissionRequest`、`PostToolUse`、`PreCompact`、
`PostCompact`、`SessionStart`、`SessionEnd`、`UserPromptSubmit`、
`SubagentStart`、`SubagentStop`、`Stop`、`Interrupt`。

定义和固定长度数组见
[`hook_config.rs:L35-L150`](https://github.com/openai/codex/blob/44dd77b71e88c78295736bffd3dc3b684c13be6d/codex-rs/config/src/hook_config.rs#L35-L150)。
RFC-001 旧稿所写“11 个事件”遗漏了 `Interrupt`，本次已更正为 12 个。

### Handler 类型

同一源码快照解析四类 handler：`command`、`mcp_tool`、`prompt` 和 `agent`
（[`hook_config.rs:L161-L200`](https://github.com/openai/codex/blob/44dd77b71e88c78295736bffd3dc3b684c13be6d/codex-rs/config/src/hook_config.rs#L161-L200)）。
当前执行路径会构造 `command` 和 `mcp_tool` handler，但明确跳过
`prompt` 与 `agent`
（[`discovery.rs:L505-L656`](https://github.com/openai/codex/blob/44dd77b71e88c78295736bffd3dc3b684c13be6d/codex-rs/hooks/src/engine/discovery.rs#L505-L656)）。
源码没有 Claude Code 的 `http` handler。

已发布的官方 [`Codex hooks`](https://developers.openai.com/codex/hooks)
页面也说明 `command` 和 `mcp_tool` 可以执行，`prompt` 和 `agent` 只解析不执行。
Phase 1A 仍只使用 `command`。这是 Claude Code 与 Codex 的共同能力，也不依赖
MCP server 已连接。

### 配置与信任边界

Codex 从活动配置层旁的 `hooks.json` 或 `config.toml` 内联 `[hooks]` 发现
配置。常用位置是 `~/.codex/hooks.json`、`~/.codex/config.toml`、
`<repo>/.codex/hooks.json` 和 `<repo>/.codex/config.toml`
（[`Advanced configuration`](https://developers.openai.com/codex/config-advanced#hooks)）。
hooks 默认启用。正式 feature key 是 `hooks`，`codex_hooks` 只作为弃用别名
保留。Phase 1A 不需要写入 `[features] codex_hooks = true`。

信任包含两层：

1. project `.codex/` 配置只有在项目受信任时才加载；用户和 system 层不依赖
   project trust
   （[`Codex hooks: where Codex looks`](https://developers.openai.com/codex/hooks#where-codex-looks-for-hooks)）。
2. 非 managed hook 还要按规范化定义的 hash 单独信任。定义变化后状态成为
   `Modified`，不会进入可执行 handler 集合，直到重新确认
   （[`discovery.rs:L666-L736`](https://github.com/openai/codex/blob/44dd77b71e88c78295736bffd3dc3b684c13be6d/codex-rs/hooks/src/engine/discovery.rs#L666-L736)、
   [`discovery.rs:L768-L823`](https://github.com/openai/codex/blob/44dd77b71e88c78295736bffd3dc3b684c13be6d/codex-rs/hooks/src/engine/discovery.rs#L768-L823)）。

Codex 的启动确认界面明确提示，受信任的 hook 可以在 sandbox 外运行，并允许
用户“继续但不信任，此时 hooks 不运行”
（[`startup_hooks_review.rs:L227-L272`](https://github.com/openai/codex/blob/44dd77b71e88c78295736bffd3dc3b684c13be6d/codex-rs/tui/src/startup_hooks_review.rs#L227-L272)）。
Drove 不得自动使用 `--dangerously-bypass-hook-trust`，也不能把未出现 signal
直接解释为 agent Idle。

## 统一信号模型

### 信号权威顺序

建议固定为：

1. **进程事实**：PTY 进程退出、用户停止、daemon 关闭。它决定终态，优先级最高。
2. **已确认激活的 hook**：描述 turn、工具和人工等待，不直接宣告进程终态。
3. **输出启发式**：仅在 hook 未激活或明确不可用时启用，并要求阈值与去抖。
4. **静默超时**：只能生成低置信度 Idle 候选，不能生成 Done。

“hook 已激活”必须由当前 session 实际收到合法信号证明，不能从配置文件存在性
推断。一旦激活，启发式仍可作为证据记录，但不得与 hook 并行写状态。

### 规范化 Signal

厂商 payload 应在 adapter 边界被压缩为稳定的内部格式。建议至少包含：

```text
version
vendor
vendor_event
scope              # root | subagent
vendor_session_id  # 可选，只用于关联，不作为 Drove 主键
vendor_turn_id     # 可选
notification_type  # 可选 allowlist
confidence
occurred_at        # 厂商提供时保留，但不作为排序依据
received_at        # daemon 接收时间，作为排序依据
delivery_id        # relay 单次投递的幂等键
```

不得把 prompt、tool input、transcript、assistant message 或任意厂商 JSON 原文
写进事件日志。持久化 payload 只保留 allowlist 中的枚举、ID、布尔值和长度等
摘要。

### 状态映射

| 信号 | 建议状态 | 约束 |
| --- | --- | --- |
| 根会话 `UserPromptSubmit` | Working | 新 turn 的确定起点 |
| 根会话 `PreToolUse`、`PostToolUse`、`PostToolUseFailure`、`PostToolBatch` | Working | 活跃心跳，也可解除 Blocked |
| `PermissionRequest` | Blocked 候选 | 仅在人类审批确实会显示时采用；自动策略处理的不应制造长期 Blocked |
| Claude `PermissionDenied` | 不进入 Blocked | 自动模式已经作出拒绝决定，agent 没有等待人工 |
| Claude `Elicitation` 或人工等待 Notification | Blocked | 只接受前述 allowlist |
| Claude `ElicitationResult`、用户新 prompt、后续工具活动 | Working | 明确解除 Blocked |
| 根会话 `Stop` | Idle 候选 | hook 同批次可能阻止 Stop，因此不能收到即迁移；需要短暂确认窗口，后续活动取消候选 |
| Claude `StopFailure` | Idle 候选并记录错误摘要 | turn 因 API 错误结束，但交互进程仍可继续使用 |
| Codex `Interrupt` | Idle 候选 | 表示 turn 中断，不表示进程退出或任务完成 |
| `TaskCompleted`、`agent_completed` | 不直接迁移 | 厂商任务对象或提示不等于 Drove session 已完成 |
| `SubagentStart` / `SubagentStop` | 根会话保持 Working | 子代理停止不能把仍在运行的根会话改为 Idle |
| `SessionEnd` | 记录，不单独决定终态 | hook 有超时、信任和丢失可能；以 PTY 进程退出为准 |
| oneshot 正常退出 | Done | 当前既有语义保持不变 |
| interactive 进程退出 | Stopped | `Stop` 只是 turn 结束；interactive 不从 hook 自动进入 Done |
| 用户停止、daemon 关闭、异常退出 | Stopped | 由进程事实决定 |

Claude 官方说明同一事件的 matching hooks 并行运行。因此 Drove 的 `Stop`
观察 hook 与另一个可能阻止 Stop 的 hook 可以同时收到事件。Idle 必须是可取消
的候选，而不是即时状态写入。确认窗口的时长、取消信号和时钟注入方式要在 spec
中固定并用确定性测试覆盖。

## Phase 1A 编码前必须定案

### 1. 状态机与单写者

每个 session 应有一个 Detector goroutine，所有 hook、进程、启发式和 timer
只向它的 channel 投递 Signal。只有该 goroutine 能提出状态迁移。

Detector 只解决单个 session 的状态决策顺序。`internal/session` 还需要一个
全局事件提交 goroutine，把所有 session 的 signal、状态、输出、输入和错误
写入同一个追加日志。事件序号只能由该提交器在写入时分配，不能由各 Detector
分别调用 `Hub.NextSeq`。

spec 必须给出完整迁移表，至少覆盖：

- `Working ↔ Blocked`；
- `Working/Blocked → Idle` 和 `Idle → Working`；
- 任意非终态到 `Stopped`；
- oneshot `Working → Done`；
- `Done`、`Stopped` 为终态；
- 重复 signal、过期 timer 和终态后的 signal 为无投影效果事件。

interactive 模式暂不提供自动 Done。若产品需要显式完成，应另行定义
`drove done` 或后续协议事件，不能把 `Stop` 或文本语义猜测升级为 Done。

### 2. 持久化与发布顺序

当前 `Agent.Transition` 先修改内存，再调用无返回值回调
（[`agent.go:L258-L275`](https://github.com/Duang777/drove/blob/88d3148b0f9caf52ddef327f56872c1384030ad2/internal/agent/agent.go#L258-L275)）。
`onStateChange` 又忽略持久化错误
（[`session.go:L505-L510`](https://github.com/Duang777/drove/blob/88d3148b0f9caf52ddef327f56872c1384030ad2/internal/session/session.go#L505-L510)）。
这会产生“内存已变、SQLite 未变”的不可恢复状态。

Phase 1A 应固定以下顺序：

1. Detector 串行接收并验证 Signal。
2. 计算零个或一个状态迁移，不修改内存。
3. 将 `agent.signal` 与对应 `agent.state_changed` 组成一个提交请求。
4. 全局事件提交器按接收顺序分配连续序号，并用同一 SQLite 事务提交 batch。
   现有 `Store.AppendEvents` 已提供连续序号和
   过期边界校验
   （[`store.go:L103-L151`](https://github.com/Duang777/drove/blob/88d3148b0f9caf52ddef327f56872c1384030ad2/internal/store/store.go#L103-L151)）。
5. 提交成功后更新内存投影。
6. 最后按序向 Hub 发布已提交事件。

提交失败时不得更新内存或 Hub，也不得用未持久化的 error event 掩盖失败。
signal 没有触发迁移时仍单独持久化，保证“为什么没变”也可审计。
输入、输出和错误事件也必须经过同一个提交器，否则它们仍可能先取得较小序号、
后写入数据库并晚于较大序号发布。

### 3. 事件兼容与回滚

恢复投影当前遇到未知事件会直接失败
（[`projection.go:L62-L100`](https://github.com/Duang777/drove/blob/88d3148b0f9caf52ddef327f56872c1384030ad2/internal/session/projection.go#L62-L100)）。
因此发布顺序必须采用 reader-first：

1. 兼容版本 A 先加入 `agent.signal` 和新版状态 payload 的读取、校验与回放，
   但 writer 默认关闭。
2. 完成“旧日志由 A 恢复”和“含新事件日志由 A 恢复”验证。
3. 版本 B 才启用 signal writer 和 Detector。

一旦 B 写入新事件，允许回滚的最低版本是 A，不能回滚到 `88d3148`。如果发布
流程不能接受这个下限，就必须先定义可逆的数据迁移；不能依赖旧二进制忽略未知
事件，因为它当前明确不会忽略。

### 4. 配置与 hook 生命周期

Phase 1A 建议只交付：

- `drove hook --vendor <vendor>` relay；
- loopback signal endpoint；
- 启动子进程时注入 session 关联环境变量；
- vendor adapter 与 Detector；
- 手工配置样例或测试隔离配置；
- hooks `off`、`auto`、`required` 三态策略，其中 `required` 在未观察到 hook
  激活时明确报错，`auto` 才允许启发式降级。

Phase 1A 不自动修改 `~/.claude`、项目 `.claude`、`~/.codex` 或项目
`.codex`。Issue #15 后续增加会话级参数和临时文件，但仍不修改这些持久配置。

可选的持久安装器若继续实施，必须满足：

- 由显式命令触发，不由 `drove up` 静默写配置；
- 使用 JSON/TOML parser 做结构化合并；
- 原子写入、写前备份、所有权标记和幂等更新；
- 卸载时只删除 Drove 拥有的节点；
- 不代替用户接受 workspace/project/hook trust；
- 不启用任何 bypass trust 参数。

### 5. Adapter 与模块边界

| 模块 | 应负责 | 不应负责 |
| --- | --- | --- |
| `cmd/drove` | 从 stdin 读取 vendor JSON，调用 relay client；命令形状合法后即使投递失败也返回 0 | 解析厂商状态语义、直接写 SQLite |
| `internal/api` | loopback、认证、大小限制、严格 JSON 解码、HTTP 状态码 | vendor 映射、状态迁移 |
| `internal/adapter` | 各厂商事件白名单、payload 解析、规范化 Signal、配置样例 | 持久化、全局状态机 |
| `internal/detect` | 信号优先级、去抖、timer、状态迁移建议 | Claude/Codex JSON 字段、HTTP |
| `internal/session` | Detector 生命周期、进程信号接入、全局事件提交、内存投影、Hub 发布 | 厂商专属条件分支 |
| `internal/event` / `internal/store` | 版本化事件与只追加事务 | 状态策略、vendor 规则 |

这样既满足厂商差异只能进入 `internal/adapter` 的项目约束，也避免 Detector
随着 Claude/Codex 字段变化而分叉。

## 回调安全与隐私

建议每个运行 session 生成独立随机 token，并通过以下环境变量传给 agent
进程和其 hook 子进程：

```text
DROVE_AGENT_ID
DROVE_SIGNAL_URL
DROVE_SIGNAL_TOKEN
```

endpoint 只监听 loopback，要求 `Authorization: Bearer ...`，使用常量时间比较
token，限制 body 大小，拒绝未知字段、无效 UTF-8、错误 vendor、已 detach 的
session 和过期 token。relay 若重试，必须复用同一个 `delivery_id`；Detector
对该键幂等，避免重复状态事件。daemon 接收顺序是权威顺序，厂商时间戳只作
证据，不能让迟到事件倒写状态。

这个 token 只解决 session 关联和误串话，不是强安全隔离：

- hook 继承环境，agent 本身可以读取 token；
- 同一 OS 用户下的其他进程可能具备观察进程环境或访问 loopback 的能力；
- Claude command hook 与受信任的 Codex hook 都以用户权限、在 agent sandbox
  之外执行。

因此，不应把 callback 当作抵御恶意 agent 或同用户恶意进程的边界。真正的
边界仍是单用户 localhost daemon、厂商自身 trust 流程、最小化环境变量和
严格的数据留存策略。

## Phase 1 验收结果

实现和回归测试覆盖以下行为：

- Claude 与 Codex adapter 使用固定、脱敏的官方 schema fixture 做契约测试。
- 表驱动测试覆盖每个允许事件、未知事件、未知 Notification、root/subagent
  scope 和缺失字段。
- 使用可注入时钟验证 Stop 去抖、取消、重复 delivery、迟到 signal 和 timer
  竞态。
- hooks active 时启发式不能写状态；hooks unavailable 时才能按阈值降级。
- Blocked 在新 prompt、工具活动或 elicitation result 后恢复为 Working。
- TaskCompleted、agent_completed 和 SubagentStop 均不能产生 Done。
- oneshot 正常退出为 Done；interactive 退出及用户停止为 Stopped。
- SQLite batch 失败时，内存状态与 Hub 均保持原值。
- 多个 session 并发产生事件时，SQLite 与 Hub 都按同一连续序号观察事件。
- 重启恢复能回放仅 signal、signal+state batch、重复 signal 和未知 payload
  version 的预定行为。
- endpoint 覆盖非 loopback、错误 token、超限 body、未知字段、过期 session
  与重复 delivery。
- Claude Code `2.1.288` 的隔离回归确认注入的 `SessionStart` 可满足
  `required`。会话目录和 settings 文件权限分别为 `0700` 与 `0600`，退出后
  Drove 删除该目录。
- Claude 用户 settings 与会话 settings 中的相同 command 只执行一次。用户
  设置 `disableAllHooks: true` 时没有原生 hook signal，`auto` 在 5 秒后进入
  fallback。
- Codex CLI `0.160.0` 通过隔离的本地 Responses SSE provider 完成真实
  oneshot turn，并调用注入的 notify relay。Drove 保存版本 2 的
  `agent-turn-complete` signal，但没有把 notify 当作 hook 激活。
- 长生命周期 relay 回归确认 fallback 中的 notify 经过 1 秒确认后进入 Idle，
  且 hook 状态保持 fallback。
- Claude 和 Codex 的显式 `signal_injection: "off"` 均不增加参数或临时文件。
  手工 Claude hook 在关闭注入后仍可满足 `required`。
- Claude 三层 settings 的运行前后哈希一致。Codex oneshot 和显式 `off`
  场景的 `config.toml` 运行前后哈希一致。Codex TUI 自己写入了 `[tui]`
  状态。事件日志没有保存测试 prompt、assistant response 或内部标题 prompt。
- 隔离 daemon 与官方形状 fixture 验证 Claude 和 Codex 的
  `Working -> Blocked -> Working -> Idle`、重复 delivery 和持久化脱敏。
- Phase 1A 本身不包含 WebSocket 输入。该能力后来由独立变更完成，Issue #4
  已关闭。

Issue #13 完成后，PTY 桥接器会立即交付原始字节块，不再等待换行。隔离回归中，
Claude Code `2.1.181` 的 3101 字节启动流、Codex CLI `0.159.2` 的 152 字节
启动流都与直接 PTY 抓取逐字节一致。Codex 的 `ESC[6n` 位于偏移 28，整段没有
换行。该结果证明了 Issue #14 的原始输入边界，但不再代表当前主干。当前
terminal actor 会应答 DSR、OSC 10/11、DA1 和 Kitty keyboard 查询。

## Issue #14 的终端屏幕结果

每个 attached 会话有一个 terminal actor 和一个 observation actor。terminal
actor 独占 x/vt controller、厂商 classifier、固定 100 ms 采样计时器和当前
snapshot。observation actor 独占 Detector。两个 actor 通过规范化 screen
observation 连接，不读取对方内部状态。

输出只有在 Store 提交成功、投影更新和 Hub 发布后，才以
`term.CommittedChunk` 进入 x/vt。这个顺序保证 screen attribution 中的 offset
和最终 output sequence 都指向已提交事实。进程退出先禁止新的 screen
observation，PTY reader 仍可提交尾部输出和更新私有 emulator。

x/vt reply pump 在首个 write 前启动。query reply 直接调用
`pty.Session.Write`，与用户输入共用完整帧写锁，但不经过 `Manager.SendInput`。
因此 reply 不产生输入审计，也不进入输出、Hub、回放、snapshot 或 explain。
只有子进程显式回显的 reply 才成为新输出。

adapter 只暴露稳定规则名、边沿、区域、静态 evidence 和确认时长。私有 matcher
留在 `internal/adapter`。Detector 在 fallback 中使用屏幕规则。hook 激活后，
Detector 只允许审批框消失和 Claude 中断两个 Spec 006 例外改变状态，并把其他
screen edge 持久化为 `suppressed`。

screen signal 和 state evidence 使用 version 3。持久数据没有屏幕文字或屏幕
hash。`drove explain` 默认读取最近 50 条脱敏 signal 和 state 事件，上限为
200。attached snapshot 最多取底部 12 行、每行 160 个 cells 和 4 KiB。snapshot
经过 signal token 打码，但没有通用密钥扫描。detach 和 daemon 重启后只返回
持久化决策。

发布继续采用 reader-first。`f361ab5` 先加入 version 3 reader，`b202e70` 才
启用 writer。写入 version 3 后，数据库不能回滚到 `f361ab5` 之前。

构建下限是 Go 1.24.2。CI 保留精确的 Go 1.24.2 lane 和当前 stable lane。x/vt
固定为 `v0.0.0-20261004011457-ad85c59fdf4e`。

32 会话基准在 Apple M5 Pro、Go 1.24.13、darwin/arm64 上得到
1.026781392 s/op 和 31.19 MiB/s 聚合吞吐。每轮共处理 32 MiB committed
output，峰值 goroutine 为 99，actor inbox 最大深度为 1，没有触发
backpressure。race 单轮是 3.185080333 s/op 和 10.05 MiB/s。完整结果见
[技术笔记](technical-notes.md#9-terminal-actor-32-session-benchmark)。

public resize、WebSocket 终端流和 attach 不属于 Issue #14。后续范围分别见
[Issue #19](https://github.com/Duang777/drove/issues/19) 和
[Issue #20](https://github.com/Duang777/drove/issues/20)。

## 一手来源

### Drove

- [`88d3148` 输入实现基线](https://github.com/Duang777/drove/commit/88d3148b0f9caf52ddef327f56872c1384030ad2)
- [`48d68bd` 会话信号注入基线](https://github.com/Duang777/drove/commit/48d68bdb9dd6bdf761983d4e3724209010d0ab60)
- [RFC-001](rfc-001-agent-state-and-control.md)
- [Issue #2：hook 状态识别](https://github.com/Duang777/drove/issues/2)
- [Issue #3：Blocked 恢复](https://github.com/Duang777/drove/issues/3)
- [Issue #4：输入注入](https://github.com/Duang777/drove/issues/4)
- [Issue #13：原始 PTY 输出块与保留](https://github.com/Duang777/drove/issues/13)
- [Issue #14：终端屏幕模型与查询应答](https://github.com/Duang777/drove/issues/14)
- [Issue #15：会话信号注入](https://github.com/Duang777/drove/issues/15)

### Claude Code

- [Hooks reference](https://code.claude.com/docs/en/hooks)
- [Hooks guide](https://code.claude.com/docs/en/hooks-guide)
- [Settings](https://code.claude.com/docs/en/settings)
- [Permissions and workspace trust](https://code.claude.com/docs/en/permissions)

### OpenAI Codex

- [Hooks](https://developers.openai.com/codex/hooks)
- [Advanced configuration](https://developers.openai.com/codex/config-advanced#hooks)
- [Configuration reference](https://developers.openai.com/codex/config-reference)
- [`HookEventsToml` 与 handler schema，固定源码快照](https://github.com/openai/codex/blob/44dd77b71e88c78295736bffd3dc3b684c13be6d/codex-rs/config/src/hook_config.rs#L35-L200)
- [handler discovery、执行支持与 trust hash，固定源码快照](https://github.com/openai/codex/blob/44dd77b71e88c78295736bffd3dc3b684c13be6d/codex-rs/hooks/src/engine/discovery.rs#L505-L823)
- [启动时 hook trust 确认，固定源码快照](https://github.com/openai/codex/blob/44dd77b71e88c78295736bffd3dc3b684c13be6d/codex-rs/tui/src/startup_hooks_review.rs#L227-L287)
