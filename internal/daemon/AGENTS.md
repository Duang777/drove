# AGENTS.md — internal/daemon

## 职责

**常驻服务生命周期**：组装 config / store / event Hub / adapter / session Manager / API server，处理优雅关闭与信号。

## 关键设计

- `Daemon.Run(ctx)`：打开 store → 构建 Hub/Registry/Manager → 启动 API server → 阻塞直到 ctx 取消或收到 SIGINT/SIGTERM。
- 优雅关闭顺序：先停 API（不再接受新连接）→ 关闭 Hub 订阅 → 关闭 store → 停止会话（agent 进程随之终止）。
- 本包不做业务逻辑，只做装配（composition root）。新依赖一律在此注入。

## 约束

- 禁止在其它包直接组装 store/hub/manager 的完整链路（除非测试）。
- 日志用 `slog`（结构化），启动/关闭的关键节点必须打日志。
- 导出类型：`Daemon`。
