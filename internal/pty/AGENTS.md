# AGENTS.md — internal/pty

## 职责

**PTY（伪终端）生命周期管理与字节流桥接**。Drove 让 agent 进程运行在真实终端中，本包负责创建 PTY、拉起子进程、读写桥接、回收资源。

## 关键设计

- `Session` 封装 `creack/pty`：`Start` 校验 `Config.Size`，通过
  `StartWithSize` 在进程启动前设置 PTY 行列，再把 master 转为 Go poller
  可管理的 nonblocking + close-on-exec 重复 fd；转换失败必须杀死并回收已启动进程。
- `Write` 使用独立的 fail-fast writer permit 和 1 秒绝对 write deadline；
  并发写入及 deadline 超时返回 `ErrWriteBackpressure`，部分写入必须返回精确字节数。
  生命周期状态与 writer permit 分离，`Close` 和自然退出不等待 writer 即可关闭写入
  admission 并让当前 deadline 立即到期。
- 读取循环使用 32 KiB 缓冲区，按带源字节偏移的块调用 `Config.OnOutput`；
  正常块不拆分合法 UTF-8 码点，EOF 原样送出无效或不完整尾部字节。
- `Config.OnOutputEnd` 在最后一个输出块后调用一次；它与 `Config.OnExit` 没有顺序保证。
- `Config.OnOutput`、`Config.OnOutputEnd` 与 `Config.OnExit` 在读取和等待
  goroutine 启动前固定，运行中不得替换。
- `creack/pty` 为子进程创建独立 session；主动停止先向 PID 对应的进程组发送
  SIGTERM，等待 `Config.TerminationGrace`，超时再发送 SIGKILL。默认宽限 5 秒。
- 直接子进程自然退出后也通过同一个幂等步骤清理仍存活的进程组后代，避免遗留
  继承 PTY 的 helper；主动关闭与自然退出不得重复或交叉执行信号升级。
- Unix 进程组在存活探测与发送信号之间消失时，`ESRCH` 与 Darwin 返回的 `EPERM`
  都按幂等完成处理；禁止继续操作可能已复用的外部进程组。
- PTY master 只在直接子进程已回收后关闭；`Close` 幂等，并等待读取、进程退出和
  全部回调及 writer 完成。自然退出仍在读取结束后关闭 master，并与主动关闭共享
  唯一的 master close。
- 进程退出码经 `WaitCh` 返回，供状态机迁移到 `Stopped`/`Done`。

## 约束

- 禁止在本包 import 厂商适配 / agent 状态机 / 存储。
- 阻塞读必须可被 Close 打断（通过关闭 pty 文件实现）。
- PTY 写入必须保持有界；不得重新把 kernel Write 放进生命周期临界区。
- 新增命令行参数处理（如环境变量透传）只允许在本包内实现。
- 导出类型：`Session`、`Config`、`Size`、`ExitInfo`。
