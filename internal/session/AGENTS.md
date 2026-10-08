# AGENTS.md — internal/session

## 职责

**会话编排层**：把 agent 状态机、PTY、适配器、事件 Hub、存储串成一条线。上层（daemon/api/CLI）只与本包交互，不直接触碰 pty / adapter / store。

## 关键设计

- `Manager` 持有：`agents`（ID→session-owned managed Agent）、`sessions`
  （ID→运行中 PTY）、原生恢复预留、event Hub、store、adapter Registry。
- 每个运行中会话持有一个 terminal actor、一个 observation actor、一个 recording actor、
  Detector State 和 signal token 的 SHA-256 digest；token 只授权该 Agent 的
  signal endpoint，并在启动失败或退出认领时失效。
- recording actor 独占源输出偏移、持久化输出偏移、有效终端尺寸和临时 attachment
  状态。容量 64 的 inbox 统一排序 output、resize、attached input、detach 和 close。
- terminal actor 独占 x/vt controller、adapter classifier、容量 64 的 inbox、
  固定 100 ms sample timer 和当前不可变 snapshot。每个 actor 有一个 query reply
  forwarder 排空 controller 的容量 16 mailbox，并通过 session 提供的 writer 在
  per-session control gate 内写 PTY；首次失败、部分写、busy 或 timeout 后记录错误并
  丢弃后续 reply，controller close 后 join。reply 不经过 `SendInput`，不产生
  `agent.input`；只有子进程显式回显的 reply 才作为新输出提交。
- 完整 Codex signal injection 成功时，recording actor 在持久化前先用 adapter
  提供的策略等长打码 OSC 9 自由文本；terminal actor 再独占一个 OSC 9 scanner，
  只读取 committed sanitized bytes，并把 adapter 已脱敏的 observations 返回
  recording actor。
- observation actor 独占容量 64 的 inbox 和一个真实计时器，一次只提交一个
  Decision；每次提交后按 Detector 返回的最早 timer ref 重置计时器，Detector
  本身不持有 goroutine 或回调。
- 一个全局 Committer goroutine 独占运行时事件序号和写入顺序。Store batch 成功后，
  Committer 才应用 Agent 投影并按序发布 Hub。workspace removal 使用 typed
  operation，在 append 后清除 working directory、关闭 Resume，再发布事件；该元数据
  事件不改变 Agent 状态的 UpdatedAt，恢复投影必须保持相同语义。
- `managedAgent` 在同一投影锁下保存 Agent 可变状态和当前 `state_changed` 序号；
  `Status.state_seq` 以十进制字符串公开。Committer 是运行时唯一写者，恢复投影从
  最后一条已接受的状态事件重建该序号。
- `Start(ctx, req)`：校验并默认 `RunMode` → 按 vendor 取适配器 → 构造 agent →
  持久化 `starting` → 以统一的 40 行 × 120 列初始尺寸创建带固定回调的 PTY →
  创建 terminal actor → 持久化 `working` → 依次放行 signal 与 PTY callback →
  等待 required hook。
- `Resume(ctx, id)` 在同一 Agent ID 下预留一次恢复，提交私有 `agent.resumed` 与 typed
  `Stopped -> Starting` 后复用 Start 的 PTY 激活路径；`Status.Resumable` 只由已停止、
  未连接、未预留、有已提交 ref 且 exact adapter 支持恢复的会话派生。恢复后的 PTY
  源偏移重新从 0 计数，持久 output offset 则从 Store 的 session boundary 继续。
- `Start` 把清理后的绝对工作目录放进 Agent 和创建事件的私有持久载荷；Hub 与公开
  replay 删除该字段。恢复投影把目录放回 Agent 与 managed record，Resume 用它配置
  PTY，`Status.Dir` 对控制客户端返回该目录。
- `StartRequest.Worktree` 存在时，Manager 在生成 Agent ID 后调用
  `internal/workspace` 创建独立 worktree，并把 Agent 工作目录切到该路径。创建事件
  持久化前的失败会回滚 worktree 和本次新建的分支；SQLite append 成功后，即使投影
  或 Hub 发布失败也不得回滚。创建事件 durable 后必须确认 preparation sidecar；确认
  失败时保留 creation reservation 并 fail-stop，由 Bootstrap 按私有 workspace metadata
  采纳。
- `Start` 从生成 Agent ID 起登记 workspace creation reservation；创建事件 durable
  后将 reservation 与 Agent/PTY 登记原子交接。durable 后的发布失败保留 reservation
  直到 daemon fail-stop，防止清理已由事件拥有的目录。
- `CleanupWorkspace` 只接受终态或无会话的 Agent，并在执行 Git 清理期间登记
  reservation 与 completion channel。`Resume` 必须拒绝同一 Agent，且
  `Status.Resumable` 在 reservation 存续期间必须为 false，避免恢复进程与目录删除
  并发。
- workspace 先持久化 removal intent，再执行物理删除。已登记 Agent 通过 typed
  Committer 提交 `session_lifecycle(workspace_removed)`。Store durable 后，
  `AcknowledgeRemoval` 才删除 sidecar。物理删除 pending 时，运行时立即关闭 Resume。
- Bootstrap 在返回 Manager 前先调用 `ReconcilePreparations`，按私有 workspace
  metadata 采纳 durable 创建并回滚无对应事件的 orphan，再调用 `ReconcileRemovals`。
  removal reconciliation 补写缺失 tombstone；如果事件已存在，则只删除 sidecar。
- 创建事件的私有 `workspace` 元数据记录仓库、路径和分支；Hub 与公开 replay 删除
  整个对象，恢复投影仍校验其中的绝对路径、分支及其路径与 working directory 一致。
- 初始终端尺寸先经 `term.NewSize` 校验，再显式转换为 `pty.Size`；
  PTY 必须在子进程启动前应用该尺寸。
- session signal injection 在创建事件前向 adapter 请求纯计划，并只在
  `<data_dir>/sessions/<agent-id>/` 原子写入私有文件；退出回调完成后清理。
- hook 环境同时携带 loopback 形状的 callback URL 与 daemon Unix socket 路径；
  session 只负责注入，实际拨号策略归 client。
- 缺失的 signal injection 根目录和会话目录使用 `0700`；既有根目录只校验类型，
  不自动修改其模式。
- 新请求默认 `interactive`；旧事件缺少 mode 时由恢复投影回退为 `oneshot`。
- PTY 回调在启动前注册；signal 与输出/退出使用独立 readiness gate，使启动期 hook
  可等待 `starting -> working`，同时防止短进程先提交错误终态。
- 运行中会话记录停止原因、退出认领状态和当前响应栅栏；REST/WS 输入、writable
  attachment 输入、terminal query reply、Detector decision commit、远程 action、
  `Stop` 和自然退出通过同一个 control gate 排序。固定锁序是
  `running.controlMu -> Manager.mu`。
- oneshot 自然成功退出为 `done`；interactive、失败退出和已登记的主动停止为 `stopped`。
- PTY 进程组清理失败优先于自然成功或主动停止原因，必须持久化 error 并进入
  `stopped`，不得把仍有不可控后代的会话记录为 `done`。
- PTY 进程组清理失败还必须把 leader PID 作为 typed
  `session_lifecycle(process_group_cleanup_failed)` 与退出终态同批持久化。运行时和恢复
  投影据此关闭 `Status.Resumable`、手动/启动恢复及 workspace cleanup；操作前只有
  `internal/pty` 以 signal 0 明确确认进程组不存在，才能先持久化
  `process_group_cleanup_completed` 再解除门禁。
- `Close()`：拒绝新 Start → 等待进行中的 Start 和 workspace cleanup → 关闭全部 PTY 并等待回调 →
  幂等关闭 recording/terminal/observation actor → 清空运行中会话索引。
- Manager 把同一 `terminationGrace` 传给新建和恢复的 PTY；关闭顺序仍按 Agent ID
  串行，不在 session 层复制信号升级逻辑。
- `Replay(sessionID)`：从 store 读取事件流供回放；仍保留的 `output.chunk` 附件被编码进
  Base64 payload，已过期的附件只返回 offset/len metadata。
- `Manager` 长期持有一个 recording archive，使 tail、timeline、Blocked lookup 和
  exact frame 共用同一只读领域入口与有界帧缓存。每个 tail 仍有独立取消域，不经过
  Hub，也不把 Store 行暴露给 API。
- `Explain(ctx, id, options)`：读取最多 200 条 `agent.signal` / `state_changed`
  envelope，解码为不透传原始 payload 的类型化摘要；仅同一 attached terminal actor
  可提供带采样时间的临时受限 screen view，退出认领或 detach 后不再返回 screen。
- `SendInput(id, data)`：校验后以 `controlMu.TryLock` fail-fast admission 完整写入已连接
  PTY，成功后仅持久化字节数，不记录输入正文，也不直接改变 Agent 状态。busy 或
  timeout 返回 `ErrInputBackpressure`；任何部分送达同时返回 `ErrInputWrite` 和
  `do not retry`，且不提交成功审计。任意正字节写入都会关闭当前状态序号的响应栅栏。
- `ActionContext` 和 `Respond` 先经过 recording actor barrier，再由 terminal actor
  立即读取 controller snapshot，并在 actor 内执行固定 callback，防止后续输出越过
  屏幕校验。callback 先生成 adapter action plan 和脱敏审计草稿，再尝试 control gate；
  持门期间复查 attached、stop/exit、Blocked、`state_seq` 和响应栅栏。
- 完整远程 action 写入后以单批次提交相邻的 `agent.action` 与 `agent.input`。零字节失败
  不关闭栅栏；部分或完整写入关闭栅栏；审计失败返回 `do not retry` 并保留栅栏。
- recording actor 校验 PTY 源偏移，跨回调等长替换 signal token；完整 Codex
  notification 注入还会等长打码未知 OSC 9 body 和安全前缀后的自由文本。处理后的
  不超过 32 KiB `output.chunk` 作为一个回调批次提交；Store 成功且 Hub 发布后，
  才用 receipt 中的 output offset、最终 sequence 和 commit time 构造
  `term.CommittedChunk` 并喂给 terminal actor。之后先提交无文本 output
  activity，再提交同批 OSC observations。
- writable attachment 采用 `latest` 尺寸策略：首个 writer 初始持有尺寸，非 owner
  只更新 proposal，成功输入在写入前应用 proposal 并在写入后晋升，owner detach
  按 actor activity ticket 选择回退。attached input 在 actor 内应用待定 resize 后获取
  同一 fail-fast control gate，attachment ID 不进入事件。
- attachment 必须显式声明 `recording` 或 `user` purpose；recording 只读且不审计，
  user 在本地建立成功后写 `attached`，在本地清理完成后写 `detached`。显式关闭、
  进程退出、`DetachAll` 和 actor shutdown 都由 recording actor 收敛为同一个
  exactly-once removal；强制清理期间 Committer 必须保持可用。
- 有效 resize 先由 terminal actor 依次应用到 PTY 与 x/vt，再提交
  `agent.resized`；重复尺寸不写事件。应用后提交失败会触发 fail-stop。
- live snapshot 仅存在内存中，每个 attachment 最多 2 Hz、channel 容量为 1，
  新值覆盖未读旧值；携带 cursor、尺寸和 `restorable:false`，在 detach 或输出结束时关闭。
- 进程退出先同步调用 `MarkProcessExited`，再终止 Detector。尾部输出仍持久化并更新
  私有 emulator，但不能产生 screen 或 terminal signal；只有 `OnOutputEnd` 完成后
  才 detach 并允许原生恢复，detach 后 snapshot 不可用。
- 所有 PTY 写入、Detector decision commit、停止和进程退出按会话串行，保证输入或
  action 审计不会落在终态之后。普通用户输入和远程 action 使用 `TryLock`，不在门后
  排队；terminal query reply、停止和退出等待当前控制操作完成。
- 恢复投影显式识别 `agent.input`、`agent.attachment`、`agent.resumed` 和
  `output.chunk`。input 与 attachment 不改变状态；`output.chunk` 与旧 `output`
  只更新已有会话的事件事实。
- 恢复投影只接受指向当前 Blocked 序号的 `agent.action`，并要求它后面紧邻同 Agent、
  同事务时间戳的有效 `agent.input`；重复、旧序号和未配对动作均视为损坏。
- 恢复投影只接受紧邻同 Agent `agent.resumed` 的 `Stopped -> Starting`；启动自动恢复
  只消费重启前非终态且已有 ref 的一次性候选，并按创建时间排序。候选等待同 Agent
  cleanup completion；自动或普通 `Resume` 消费此候选时，恢复进程及 required hook
  均成功后必须用后台上下文持久化 `session_lifecycle(startup_resume_completed)`，只有
  该事件 durable 后才清除内存资格并释放 resume reservation，避免短命进程退出后
  workspace tombstone 抢先提交。取消等待、PTY 启动失败、required hook 失败及完成事件
  未落库都保留候选。用户停止仍在进程组清理失败时以同批
  `session_lifecycle(startup_resume_cancelled)` 持久取消候选，不能在 cleanup fence
  解除后再次自动恢复。daemon 重启生成并持久化的 recovery `Stopped` 在投影读到完成
  或取消事件前始终保留该资格，连续启动失败不能把候选静默降级为普通停止会话。
- 信号与状态证据 reader 同时接受 v1、v2、typed screen v3 和 typed terminal v4；v2 的 notify
  只在 fallback 下确认 Idle。未知补充版本按既有计数策略跳过，已知畸形版本报错。
  adapter 标记为忽略的厂商内部通知不提交事件。
- 状态决策：进程退出决定终态；激活后的 hook 决定 turn 状态并只接受规范中的两个
  screen 例外；off/fallback 使用 screen evidence 和无文本 output activity。
  signal 与对应状态迁移必须同批提交。

## 约束

- 禁止在 session 之外创建 agent 或 PTY 会话。
- 事件必须经 Committer **先落库、再改投影、最后发布**；Store 失败后 Manager 通过 `Fatal()` 触发 daemon fail-stop。
- 会话关闭必须幂等（多次 Close 不 panic、不泄漏 goroutine）。
- 临时注入路径必须二次校验并拒绝 symlink；adapter 不得直接操作文件系统。
- 导出类型：`Manager`、`ManagerOption`、`StartRequest`、`Status`、
  `StateSeq`、`WorktreeRequest`、`ActionRequest`、`ActionResult`、`ActionContext`、
  `ExplainOptions`、`ExplainEvent`、`ExplainScreen`、`Explanation`、
  `AttachmentID`、`AttachmentMode`、`AttachmentPurpose`、`AttachmentOptions`、
  `TerminalAttachment`、`LiveSnapshot`。
