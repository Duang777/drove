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
- 屏幕规则的确认时长必须与 `detect.ScreenRuleConfirmation` 完全一致；适配器只声明
  稳定性元数据，不决定状态或信号权威。
- Claude/Codex 私有 normalizer 把厂商 hook JSON 压缩成 `detect.Signal`；未知事件拒绝，
  prompt、tool input、transcript 和原始 JSON 不得越过本包。
- Codex legacy notify 只保留 thread/turn ID 和枚举证据；内部任务标题 turn
  返回可忽略结果，不得激活 hook 或写审计事件。
- `Registry` 按厂商标识注册实现；`For(vendor)` 返回实现，未知厂商回退
  `generic`，即用户命令直接跑在 PTY 里且没有 screen rules。
- 内置厂商：`claude`（claude CLI）、`codex`（codex CLI）、`generic`。ACP 厂商作为预留条目（`acp` 尚未启用）。

## 约束

- 禁止在适配器之外引用厂商名做分支判断；新厂商只在本包加文件与注册。
- `interactive` / `oneshot` 的厂商参数只允许在本包维护；自定义命令覆盖与退出语义由 session 负责。
- 屏幕规则必须使用真实脱敏终端帧测试，覆盖命中、清除、误报和分块输入。
- adapter 不清洗或改写持久化及流式输出；分类器只读取 `term.Snapshot`。
- 状态判断只输出提示，最终迁移决定权在 `detect.Detector`。
- `generic` 的屏幕分类器为空；Claude/Codex 的稳定规则名和静态证据必须保持可审计，
  不得把匹配文本、正则捕获或屏幕内容放进提示。
- hook 查找必须使用 `Registry.Lookup` 精确匹配，禁止回退到 generic。
- signal injection 同样只允许精确 adapter；参数冲突必须返回显式错误，禁止覆盖
  调用方提供的 `--settings`、`--bare` 或 Codex `notify`。
- 导出类型以 `Registry`、`Entry`、`Runner`、`ScreenClassifier`、
  `ScreenHint`、`HookInput`、`HookNormalizer` 和 `SignalInjector` 为核心。
