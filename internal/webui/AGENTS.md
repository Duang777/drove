# AGENTS.md - internal/webui

## 职责

**内嵌 Web 控制台资源**：把 `web/` 的 Vite 生产构建作为只读文件系统嵌入 daemon。

## 关键设计

- `dist/` 是经过 `npm --prefix web run build` 生成并提交的生产资源，保证干净 checkout
  只运行 Go 工具链也能构建完整 daemon。
- `FS()` 只暴露 `dist/` 子树；HTTP 路由、认证、缓存和 SPA fallback 由
  `internal/api` 决定。
- `go generate ./internal/webui` 从仓库内 `web/` 重新生成资源。

## 约束

- 本包不得依赖 API、daemon 或 session。
- 禁止运行时读取工作目录下的 `web/dist`，安装后的二进制必须自包含。
- 生成目录必须与前端源码同步提交。
