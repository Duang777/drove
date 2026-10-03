# AGENTS.md — internal/adapter

## 职责

**跨厂商适配层**。这是唯一允许出现厂商专属逻辑（命令名、启动参数、状态启发式、hook JSON）的包。上层只接收 `detect.Signal` 和脱敏输出提示。

## 关键设计

- `Runner` 接口：`Command(mode) (name, args)` —— 把已校验的 `agent.RunMode` 翻译为厂商 CLI 命令。
- `SignalInjector` 根据已解析的 relay 路径、运行模式和调用方参数生成完整启动参数及
  session 私有文件描述；只做纯计划，不读写文件。
- `Heuristic` 接口：`Classify(line string) (OutputHint, bool)`，只返回标准信号类型、
  置信度和脱敏证据，不返回 Agent 状态。
- Claude/Codex 私有 normalizer 把厂商 hook JSON 压缩成 `detect.Signal`；未知事件拒绝，
  prompt、tool input、transcript 和原始 JSON 不得越过本包。
- Codex legacy notify 只保留 thread/turn ID 和枚举证据；内部任务标题 turn
  返回可忽略结果，不得激活 hook 或写审计事件。
- `Entry.Classify` 在调用厂商 Heuristic 前移除常见 CSI、OSC、控制字符串和单字符 ESC 序列；原始输出不在本包修改。
- `Registry` 按厂商标识注册实现；`For(vendor)` 返回实现，未知厂商回退 `generic`（即用户命令直接跑在 PTY 里，无启发式）。
- 内置厂商：`claude`（claude CLI）、`codex`（codex CLI）、`generic`。ACP 厂商作为预留条目（`acp` 尚未启用）。

## 约束

- 禁止在适配器之外引用厂商名做分支判断；新厂商只在本包加文件与注册。
- `interactive` / `oneshot` 的厂商参数只允许在本包维护；自定义命令覆盖与退出语义由 session 负责。
- 启发式规则必须可测：每个模式有单测（`claude_test.go` 等），命中/误报都要覆盖。
- 终端清洗只服务分类视图，不得改写持久化或流式事件 payload。
- 状态判断只输出提示，最终迁移决定权在 `detect.Detector`。
- hook 查找必须使用 `Registry.Lookup` 精确匹配，禁止回退到 generic。
- signal injection 同样只允许精确 adapter；参数冲突必须返回显式错误，禁止覆盖
  调用方提供的 `--settings`、`--bare` 或 Codex `notify`。
- 导出类型以 `Registry`、`Entry`、`Runner`、`Heuristic`、`OutputHint`、
  `HookInput`、`HookNormalizer` 和 `SignalInjector` 为核心。
