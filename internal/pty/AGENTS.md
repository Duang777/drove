# AGENTS.md — internal/pty

## 职责

**PTY（伪终端）生命周期管理与字节流桥接**。Drove 让 agent 进程运行在真实终端中，本包负责创建 PTY、拉起子进程、读写桥接、回收资源。

## 关键设计

- `Session` 封装 `creack/pty`：`Start` 创建 PTY 并启动命令；`Write` 向 agent 注入输入；读取循环把输出**按行切分**后经 `Config.OnOutput` 回调上抛（事件化由上层负责）。
- `Config.OnOutput` 与 `Config.OnExit` 在读取和等待 goroutine 启动前固定，运行中不得替换。
- 所有资源（pty fd、进程句柄）在 `Close` 中集中回收；`Close` 幂等，并等待读取、进程退出和回调全部完成。
- 进程退出码经 `WaitCh` 返回，供状态机迁移到 `Stopped`/`Done`。

## 约束

- 禁止在本包 import 厂商适配 / agent 状态机 / 存储。
- 阻塞读必须可被 Close 打断（通过关闭 pty 文件实现）。
- 新增命令行参数处理（如环境变量透传）只允许在本包内实现。
- 导出类型：`Session`、`Config`、`ExitInfo`。
