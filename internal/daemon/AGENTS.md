# AGENTS.md — internal/daemon

## 职责

**常驻服务生命周期**：组装 config / store / event Hub / adapter / session Manager / API server，处理优雅关闭与信号。

## 关键设计

- `Daemon.Run(ctx)`：校验配置并打开本地凭据控制器 → 打开 store → 构建
  Hub/Registry/Manager → 恢复投影 → 打开 Unix socket 和可选 loopback TCP listener
  → 把 signal origin 一次性配置给 Manager → 启动 API server → 阻塞直到 ctx
  取消或收到 SIGINT/SIGTERM。
- 所有 listener 必须在任一 Serve goroutine 启动前创建成功；Unix listener 始终启用，
  TCP 可由配置关闭。
- Unix listener 只接受 `drove.local` Host；TCP listener 从实际端口派生
  `127.0.0.1`、`localhost` 和 `[::1]` Host 与同源 Origin 白名单。
- 浏览器 listener 同源提供 `internal/webui` 的嵌入式生产构建；运行时不依赖源码目录
  或 Node。
- 自动恢复开启时，API server 必须先进入 listener `Accept`，再按创建时间顺序调用
  Manager 的同 ID 原生恢复；单个失败只记录 Agent ID 与脱敏错误并继续。
- 优雅关闭顺序：先停 API（不再接受新连接）→ 停止会话并等待 PTY 回调 → 关闭 Hub 订阅 → 关闭 store。
- session Committer 报告运行时持久化或投影失败时立即走同一关闭路径，禁止 daemon 在不可恢复状态下继续服务。
- Store 打开后、投影恢复前执行一次严格的输出附件保留清理；首次失败中止启动。
  启动成功后每 24 小时重试，计划清理失败只记录警告。
- 保留循环在 Store 关闭前取消并等待退出；当前时间、tick 和 prune 操作可由聚焦测试注入。
- 启动时用 `Lstat` 校验数据目录和数据库类型；既有路径开放 group/other 权限时记录
  结构化警告，但不自动修改模式。
- 启动时清理无法跨重启存活的 session 注入目录，并解析同目录或 PATH 中的
  `drove` relay，再通过 ManagerOption 注入；厂商参数仍由 adapter 决定。
- 信号取消与 Serve 异常共用关闭路径；任一步失败都继续清理剩余资源，并汇总返回错误。
- 本包不做业务逻辑，只做装配（composition root）。新依赖一律在此注入。

## 约束

- 禁止在其它包直接组装 store/hub/manager 的完整链路（除非测试）。
- 日志用 `slog`（结构化），启动/关闭的关键节点必须打日志。
- 导出类型：`Daemon`。
