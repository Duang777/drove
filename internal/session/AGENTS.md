# AGENTS.md — internal/session

## 职责

**会话编排层**：把 agent 状态机、PTY、适配器、事件 Hub、存储串成一条线。上层（daemon/api/CLI）只与本包交互，不直接触碰 pty / adapter / store。

## 关键设计

- `Manager` 持有：`agents`（ID→*agent.Agent）、`sessions`（ID→运行中 PTY）、event Hub、store、adapter Registry。
- 每个运行中会话持有一个 observation actor、Detector State 和 signal token 的 SHA-256 digest；
  token 只授权该 Agent 的 signal endpoint，并在启动失败或退出认领时失效。
- observation actor 独占容量 64 的 inbox 和真实计时器，一次只提交一个 Decision；
  Detector 本身不持有 goroutine 或回调。
- 一个全局 Committer goroutine 独占运行时事件序号和写入顺序：Store batch 成功后才应用 Agent 投影并按序发布 Hub。
- `Start(ctx, req)`：校验并默认 `RunMode` → 按 vendor 取适配器 → 构造 agent → 持久化 `starting` → 创建带固定回调的 PTY → 登记会话并持久化 `working` → 放行输出和退出回调。
- session signal injection 在创建事件前向 adapter 请求纯计划，并只在
  `<data_dir>/sessions/<agent-id>/` 原子写入私有文件；退出回调完成后清理。
- 新请求默认 `interactive`；旧事件缺少 mode 时由恢复投影回退为 `oneshot`。
- PTY 回调在启动前注册；signal 与输出/退出使用独立 readiness gate，使启动期 hook
  可等待 `starting -> working`，同时防止短进程先提交错误终态。
- 运行中会话记录停止原因和退出认领状态；`Stop`、`Close` 与自然退出通过同一个锁确定唯一终态。
- oneshot 自然成功退出为 `done`；interactive、失败退出和已登记的主动停止为 `stopped`。
- `Close()`：拒绝新 Start → 等待进行中的 Start → 关闭全部 PTY 并等待回调 → 清空运行中会话索引。
- `Replay(sessionID)`：从 store 读取事件流供回放（CLI `log` 命令 / API）。
- `SendInput(id, data)`：校验并完整写入已连接 PTY，成功后仅持久化字节数，不记录输入正文，也不直接改变 Agent 状态。
- `onOutput` 仅替换当前会话 signal token 后持久化并发布文本，再把 adapter 的独立
  清洗分类视图交给 Detector；回放和订阅 payload 不受 ANSI 清洗影响。
- 输入写入和进程退出按会话串行，保证完整输入审计不会落在终态之后；PTY 输出不参与该锁。
- 恢复投影显式识别 `agent.input`，但该审计事件不创建会话、不改变状态或时间戳。
- 信号与状态证据 reader 同时接受 v1 和 v2；v2 的 notify 只在 fallback 下确认
  Idle。adapter 标记为忽略的厂商内部通知不提交事件。
- 状态决策：进程退出决定终态；激活后的 hook 决定 turn 状态；只有进入
  fallback 后才使用达到阈值的启发式。signal 与对应状态迁移必须同批提交。

## 约束

- 禁止在 session 之外创建 agent 或 PTY 会话。
- 事件必须经 Committer **先落库、再改投影、最后发布**；Store 失败后 Manager 通过 `Fatal()` 触发 daemon fail-stop。
- 会话关闭必须幂等（多次 Close 不 panic、不泄漏 goroutine）。
- 临时注入路径必须二次校验并拒绝 symlink；adapter 不得直接操作文件系统。
- 导出类型：`Manager`、`ManagerOption`、`StartRequest`、`Status`。
