# AGENTS.md — internal/term

## 职责

**无厂商逻辑的终端能力**。封装固定版本的 x/vt，负责屏幕仿真、终端查询应答、
不可变快照与流式控制序列清洗。

## 关键设计

- `Controller` 是 x/vt、x/ansi 与 ultraviolet 类型的唯一边界。
- reply pump 必须在首次 `Write` 前就绪；查询应答以复制后的 `ReplyFrame` 非阻塞写入
  容量 16 的 `Replies()` mailbox。mailbox 满时丢弃新帧并通过容量 1 的 `Errors()`
  报告 `ErrReplyBackpressure`，不得阻塞 emulator、snapshot 或 `Close`。
- `Snapshot` 只复制可见 cell 的内容与宽度，不包含样式、链接、标题或 scrollback。
- 对外快照视图最多返回底部 12 行、每行 160 cells 和 4 KiB UTF-8 数据。
- 快照调用方只能依赖上游的 signal token 打码；本包不做通用密钥扫描。
- `Stripper` 只服务 `drove log --plain` 等纯文本回放调用方；screen classifier
  不使用它。
- `Stripper` 跨 `Feed` 保留不完整的 ESC、CSI、OSC、DCS、SOS、PM 和 APC 状态。
- `Flush` 丢弃未结束的控制序列并重置解析器。
- 普通 UTF-8 与无效的非控制字节都按原字节保留。
- `Strip` / `StripString` 是完整输入的一次性接口。

## 约束

- 本包不得依赖 adapter、session、PTY、event 或 store。
- 不得向包外暴露 x/vt、x/ansi 或 ultraviolet 类型。
- 查询应答只进入 controller 自有 mailbox，不得进入快照、输出或输入审计路径。
- 清洗器只删除控制序列，不解释其语义。
