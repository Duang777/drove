# AGENTS.md — internal/adapter

## 职责

**跨厂商适配层**。这是唯一允许出现厂商专属逻辑（命令名、启动参数、屏幕规则、
hook JSON）的包。上层只接收规范化 hook signal 和 screen hint。

## 关键设计

- `Runner` 接口：`Command(mode) (name, args)` —— 把已校验的 `agent.RunMode` 翻译为厂商 CLI 命令。
- `SignalInjector` 根据已解析的 relay 路径、运行模式和调用方参数生成完整启动参数及
  session 私有文件描述；只做纯计划，不读写文件。
- `Entry.NewScreenClassifier` 为每个 session 创建独立的有状态分类器；规则定义、
  matcher、区域选择和边沿记忆都留在本包，只输出复制后的 `ScreenHint`。
- approval screen rule 同时保存该厂商的 `approve` / `deny` / `reply` PTY 字节映射；
  action plan 只匹配调用时提供的当前 `term.Snapshot`，并返回输入副本。reply 在组装前
  必须通过 UTF-8、trim 后 1..4096 字节和无 C0/C1 控制字符校验；approve/deny 不接受
  reply。
- 屏幕规则的确认时长必须与 `detect.ScreenRuleConfirmation` 完全一致；适配器只声明
  稳定性元数据，不决定状态或信号权威。
- Claude/Codex 私有 normalizer 把厂商 hook JSON 压缩成 `detect.Signal`；未知事件拒绝，
  prompt、tool input、transcript 和原始 JSON 不得越过本包。
- Codex legacy notify 只保留 thread/turn ID 和枚举证据；内部任务标题 turn
  返回可忽略结果，不得激活 hook 或写审计事件。
- Codex session plan 原子注入 legacy notify 与 approval-only OSC 9 配置；四个
  managed config key 中任一被调用方设置时，整个计划返回冲突。
- terminal notification normalizer 只接受 Codex 0.160.0 的三个固定 approval
  前缀，并输出带 committed attribution 的枚举信号；正文不得越过本包。
- 同一个 Codex normalizer 提供 session-local `term.OSC9Sanitizer` 工厂和安全
  前缀；adapter 决定可保留语义，streaming framing 与等长替换仍由 `internal/term`
  实现。
- `Registry` 按厂商标识注册实现；`For(vendor)` 返回实现，未知厂商回退
  `generic`，即用户命令直接跑在 PTY 里且没有 screen rules。
- 内置厂商：`claude`（claude CLI）、`codex`（codex CLI）、`generic`。ACP 厂商作为预留条目（`acp` 尚未启用）。

## 约束

- 禁止在适配器之外引用厂商名做分支判断；新厂商只在本包加文件与注册。
- `interactive` / `oneshot` 的厂商参数只允许在本包维护；自定义命令覆盖与退出语义由 session 负责。
- 屏幕规则必须使用真实脱敏终端帧测试，覆盖命中、清除、误报和分块输入。
- adapter 不直接清洗或改写持久化及流式输出；只向 session 提供厂商专属的
  sanitizer policy。分类器只读取 `term.Snapshot`。
- 状态判断只输出提示，最终迁移决定权在 `detect.Detector`。
- `generic` 的屏幕分类器为空；Claude/Codex 的稳定规则名和静态证据必须保持可审计，
  不得把匹配文本、正则捕获或屏幕内容放进提示。
- hook 查找必须使用 `Registry.Lookup` 精确匹配，禁止回退到 generic。
- signal injection 同样只允许精确 adapter；参数冲突必须返回显式错误，禁止覆盖
  调用方提供的 `--settings`、`--bare` 或 Codex managed notification key。
- 导出类型以 `Registry`、`Entry`、`Runner`、`ScreenClassifier`、
  `ScreenHint`、`ApprovalActionPlan`、`HookInput`、`HookNormalizer`、`SignalInjector` 和
  `TerminalNotificationNormalizer` 为核心。
