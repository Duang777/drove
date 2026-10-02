# AGENTS.md — cmd/droved

## 职责

**常驻 daemon 入口**：加载配置 → 构造 `daemon.Daemon` → 运行直到信号终止。

## 关键设计

- 支持 `--config` 指定配置文件路径；默认 `~/.drove/config.json`。
- 信号处理在 `daemon.Run` 内部完成（SIGINT/SIGTERM 优雅关闭）。
- 退出码：0 正常退出 / 1 配置或启动错误。

## 约束

- main 只做装配与退出码映射，业务在 internal/daemon。
- 禁止引入交互逻辑（daemon 是非交互服务）。
