# AGENTS.md — internal/cliattach

## 职责

**本地终端 attachment runner**：独占 `drove attach` 的 TTY lease、raw mode、
stdin/stdout pumps、resize signal 和退出清理。Cobra 只解析参数并调用 `Run`。

## 关键设计

- `Run` 只接受具体 `client.Client`；`Options` 可携带调用方已经租用的 stdin/stdout，
  未指定时使用进程标准流。测试通过包内 runner dependencies 替换 stream、TTY、
  cancel reader 和 signal source。
- stdin 必须是终端。runner 保存原状态、进入 raw mode，并在所有退出路径恢复一次。
- stdin 每次最多读取 32 KiB；UTF-8 不完整后缀跨读取保留，发送请求保持合法 UTF-8
  且不超过 session 的 64 KiB 上限。
- Ctrl-Q 只触发本地 detach，不发送给远端；Ctrl-C 和其他终端字节原样发送。
- writable attach 发送初始 viewport 并监听 `SIGWINCH`；read-only attach 不读取尺寸、
  不注册 resize signal，也不发送输入。
- remote EOF、context cancellation 和任一 pump 失败都会取消 reader、关闭 stream、
  停止 signal，并等待全部 pump 退出后释放资源。
- 本地退出只关闭 attachment stream，不停止远端 Agent。

## 约束

- 禁止依赖 `internal/session`、PTY 实现或 daemon 内部对象；只使用
  `internal/client` 的公开 v2 terminal API。
- stdout 只写远端原始终端字节，不添加状态文案。
- 清理必须幂等，且不得遗留阻塞 stdin read 或 signal subscription。
- 导出类型：`Options`。
