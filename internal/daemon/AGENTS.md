# AGENTS.md — internal/daemon

## 职责

**常驻服务生命周期**：组装 config / store / event Hub / adapter / session Manager / API server，处理优雅关闭与信号。

## 关键设计

- `Daemon.Run(ctx)`：校验配置并确保本地控制令牌 → 打开 store → 构建
  Hub/Registry/Manager，并注入 hook policy 与 loopback signal URL → 启动 API
  server → 阻塞直到 ctx 取消或收到 SIGINT/SIGTERM。
- 优雅关闭顺序：先停 API（不再接受新连接）→ 停止会话并等待 PTY 回调 → 关闭 Hub 订阅 → 关闭 store。
- session Committer 报告运行时持久化或投影失败时立即走同一关闭路径，禁止 daemon 在不可恢复状态下继续服务。
- 信号取消与 Serve 异常共用关闭路径；任一步失败都继续清理剩余资源，并汇总返回错误。
- 本包不做业务逻辑，只做装配（composition root）。新依赖一律在此注入。

## 约束

- 禁止在其它包直接组装 store/hub/manager 的完整链路（除非测试）。
- 日志用 `slog`（结构化），启动/关闭的关键节点必须打日志。
- 导出类型：`Daemon`。
