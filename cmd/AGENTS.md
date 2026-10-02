# AGENTS.md — cmd

## 职责

**可执行程序入口目录**。每个子目录是一个独立二进制；业务逻辑一律下沉到 `internal/`，main 包只做装配与退出码映射。

## 当前入口

| 目录 | 二进制 | 职责 |
|---|---|---|
| `cmd/drove` | `drove` | CLI 主程序（daemon 客户端，自动拉起 daemon） |
| `cmd/droved` | `droved` | 常驻 daemon（REST + WebSocket API） |

## 约束

- 新增二进制必须先写本目录下的 `AGENTS.md`（在对应子目录）。
- 禁止在 cmd 中实现业务逻辑；复用 `internal/*` 的公开 API。
- 退出码语义保持全仓库一致：0 成功 / 1 用户错误 / 2 运行时错误。
