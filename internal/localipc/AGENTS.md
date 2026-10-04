# AGENTS.md - internal/localipc

## 职责

**本机进程通信边界**：创建并保护 daemon Unix socket，并为可信本机客户端提供
HTTP-over-Unix 传输。

## 关键设计

- socket 固定为 `$DataDir/run/droved.sock`；`run` 目录必须为 `0700`，socket
  必须为 `0600`。
- `droved.lock` 使用非阻塞进程锁，避免两个 daemon 竞争删除或绑定同一 socket。
- 仅在确认既有 socket 无监听者时删除陈旧 socket；其它文件类型一律拒绝。
- listener 在 `Accept` 后读取平台 peer credential，只把当前 UID 的连接交给
  HTTP server。
- HTTP 客户端使用固定 authority `drove.local`，实际连接始终拨到 Unix socket。

## 约束

- 本包只管理 Unix listener、peer credential 和 HTTP transport，不依赖
  daemon、API、auth、session 或 config。
- 权限或 peer credential 校验失败不得回退到 TCP。
- 支持平台为 Darwin 和 Linux；平台 syscall 细节必须留在独立文件。
