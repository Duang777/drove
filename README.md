# Drove

> **herdr 让 Agent 活着，Drove 让它们往对的方向跑。**

Drove 是一个**跨厂商 Agent 指挥台（control plane）**：在一个持久化工作区里同时运行、观察、回放多个 AI coding agent，并对每个 agent 实时识别状态（Working / Blocked / Done / Idle）。

- 模型无关、厂商无关：Claude Code、Codex、任意 CLI agent 都能接入
- 事件溯源：每个会话都是可回放的事件流，断电不丢活
- 常驻 daemon + WebSocket 事件流：关掉终端，agent 继续跑；从任何机器回来都在原地

## 快速开始

从源码构建需要 Go 1.24.2 或更高版本。终端控制器固定使用
`github.com/charmbracelet/x/vt`
`v0.0.0-20261004011457-ad85c59fdf4e`。该版本修复了早期版本的 DSR
坐标问题并提供查询应答接口，因此项目最低 Go 版本与其要求保持一致。

```bash
make build
./bin/drove init          # 初始化工作区与配置
./bin/drove up claude     # 起一个 Claude Code agent
./bin/drove ps            # 查看全部 agent 状态
./bin/drove send <id> "继续" # 向运行中的 agent 发送一行输入
./bin/drove log <id>      # 回放某 agent 的事件流
```

需要精确保留换行时，可从标准输入发送：

```bash
printf '继续\n' | ./bin/drove send <id> --stdin
```

输入审计只记录字节数，不保存输入正文。daemon 只监听 loopback。它在数据目录
生成 `0600` 控制令牌，并要求 REST 与 WebSocket 客户端使用该令牌。

## 输出回放与保留

新会话把 PTY 输出保存为带字节偏移的 `output.chunk` 事件。旧数据库中的
`output` 行事件仍可读取，回放时会为每行补一个换行。

```bash
./bin/drove log <id>          # 原始终端字节，保留 ANSI 和无效字节
./bin/drove log <id> --plain  # 流式移除终端控制序列
```

原始输出默认保留 30 天。`drove init` 生成以下配置。设为 `0` 表示永久保留：

```json
{
  "storage": {
    "output_retention_days": 30
  }
}
```

清理只删除 `output.chunk` 的字节附件，事件序号、时间、offset 和长度 metadata
保持不变，因此状态投影和全局序号仍可恢复。过期字节不会在 `drove log` 中生成
占位文本。清理启用 SQLite `secure_delete` 并截断 WAL，但不执行 `VACUUM`，
所以数据库文件已经分配的大小可能不变。

终端屏幕仿真、查询应答和屏幕状态规则仍由
[Issue #14](https://github.com/Duang777/drove/issues/14) 跟踪。数据库一旦包含
`output.chunk`，可回滚的最低版本是 reader-first 提交 `d11f6c3`。

## 接入状态 hooks

Drove 默认只为自己启动的进程注入状态上报，不修改用户或项目配置：

- Claude Code 通过会话专用的 `--settings` 文件注入完整 command hooks，收到
  `SessionStart` 后成为状态权威。
- Codex 通过进程级 `notify` 上报 turn 结束。notify 只能确认 Idle，不会被误当成
  完整 hooks，因此 Working 和 Blocked 仍由 fallback 或用户已配置的原生 hooks
  判断。

两种路径都继承当前会话的 Agent ID、signal URL 和随机 token。Claude 临时
目录位于 `<data_dir>/sessions/<agent-id>/`，目录权限为 `0700`，文件权限为
`0600`。进程退出后，Drove 删除该目录。

通过 `drove up` 选择会话策略：

```bash
drove up claude --hooks auto
drove up claude --hooks off
drove up claude --hooks required
```

`auto` 是 Claude 和 Codex 的默认值。Drove 等待 5 秒，未收到合法原生 hook
时启用终端启发式。Codex notify 仍可在 fallback 中确认 Idle。`off` 不注入
状态上报，并立即使用启发式。`required` 在 5 秒内未收到合法原生 hook 时停止
会话并返回错误。不支持 hook 的适配器默认使用 `off`，且不能选择 `required`。

可按厂商关闭自动注入：

```json
{
  "agents": {
    "claude": {"signal_injection": "off"},
    "codex": {"signal_injection": "off"}
  }
}
```

`drove hook` 仅上报观察结果。有效命令即使投递失败也返回 0，因此不会阻断
厂商动作。单次 JSON 上限为 1 MiB。事件日志不保存原始 payload、prompt、
tool input、transcript path 或 capability token。

Drove 不修改 Claude Code 或 Codex 的持久配置，也不绕过 workspace、project
或 hook trust。手工配置原生 hooks 仍受支持，详见
[状态 hook 配置指南](docs/hooks.md)。

## 架构一览

```
cmd/drove (CLI/TUI)  ──WebSocket──▶  internal/daemon
                                        │
                                        ▼
                              internal/session
                       ┌────────┬───────┼─────────┐
                       ▼        ▼       ▼         ▼
                internal/detect │ internal/event internal/store
                (每会话信号融合) │ (事件Hub/扇出) (SQLite 事件日志)
                                ▼
                      internal/adapter
                 (claude / codex / generic / ACP)
                                │
                                ▼
                      internal/pty → agent 进程

web/ (React/TS 控制台)  ──REST + WebSocket──▶  internal/daemon
```

## Web 控制台（MVP 骨架）

```bash
cd web
npm install
npm run dev        # http://localhost:5173，需 daemon 已运行
```

实时事件走 WebSocket（自动重连），回放走 REST；dev server 将 `/api` 与 `/ws` 代理到本地 daemon。

## 开发

```bash
make test     # 单测 + race + 覆盖率
make vet      # 静态检查
```

每个目录都有 `AGENTS.md`，面向 AI agent 协作者说明该目录职责与约束。详见根目录 `AGENTS.md`。

## 许可

Apache-2.0，见 [LICENSE](./LICENSE)。
