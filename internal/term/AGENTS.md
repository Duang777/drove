# AGENTS.md — internal/term

## 职责

**终端字节流辅助能力**。提供无厂商逻辑的流式控制序列清洗，供 session 派生文本和
adapter 一次性分类视图复用。

## 关键设计

- `Stripper` 跨 `Feed` 保留不完整的 ESC、CSI、OSC、DCS、SOS、PM 和 APC 状态。
- `Flush` 丢弃未结束的控制序列并重置解析器。
- 普通 UTF-8 与无效的非控制字节都按原字节保留。
- `Strip` / `StripString` 是完整输入的一次性接口。

## 约束

- 本包不模拟屏幕、光标、终端查询或响应。
- 本包不得依赖 adapter、session、PTY、event 或 store。
- 清洗器只删除控制序列，不解释其语义。
