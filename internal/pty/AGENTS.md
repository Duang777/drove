# AGENTS.md — internal/pty

## 职责

**PTY（伪终端）生命周期管理与字节流桥接**。Drove 让 agent 进程运行在真实终端中，本包负责创建 PTY、拉起子进程、读写桥接、回收资源。

## 关键设计

- `Session` 封装 `creack/pty`：`Start` 创建 PTY 并启动命令；`Write` 在同一临界区内完整写入一段输入或返回已写入字节数与错误。
- 读取循环使用 32 KiB 缓冲区，按带源字节偏移的块调用 `Config.OnOutput`；
  正常块不拆分合法 UTF-8 码点，EOF 原样送出无效或不完整尾部字节。
- `Config.OnOutputEnd` 在最后一个输出块后调用一次；它与 `Config.OnExit` 没有顺序保证。
- `Config.OnOutput`、`Config.OnOutputEnd` 与 `Config.OnExit` 在读取和等待
  goroutine 启动前固定，运行中不得替换。
- 主动停止由 `Close` 回收资源；自然退出在读取结束后关闭 PTY master。`Close` 幂等，并等待读取、进程退出和全部回调完成。
- 进程退出码经 `WaitCh` 返回，供状态机迁移到 `Stopped`/`Done`。

## 约束

- 禁止在本包 import 厂商适配 / agent 状态机 / 存储。
- 阻塞读必须可被 Close 打断（通过关闭 pty 文件实现）。
- 新增命令行参数处理（如环境变量透传）只允许在本包内实现。
- 导出类型：`Session`、`Config`、`ExitInfo`。
