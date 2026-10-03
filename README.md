# Drove

> **herdr 让 Agent 活着，Drove 让它们往对的方向跑。**

Drove 是一个**跨厂商 Agent 指挥台（control plane）**：在一个持久化工作区里同时运行、观察、回放多个 AI coding agent，并对每个 agent 实时识别状态（Working / Blocked / Done / Idle）。

- 模型无关、厂商无关：Claude Code、Codex、任意 CLI agent 都能接入
- 事件溯源：每个会话都是可回放的事件流，断电不丢活
- 常驻 daemon + WebSocket 事件流：关掉终端，agent 继续跑；从任何机器回来都在原地

## 快速开始

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

## 接入状态 hooks

Drove 启动 Claude Code 或 Codex 时会注入当前会话的 signal URL 和随机 token。
在厂商的受信任 hook 配置中，把需要的事件绑定到以下 command handler：

```bash
drove hook --vendor claude
drove hook --vendor codex
```

至少绑定会话开始、用户提交、工具活动、权限请求、Stop 和会话结束事件。Drove
不修改 Claude Code 或 Codex 的配置，也不绕过 workspace 或 hook trust。

每个会话的启动请求可通过 `hooks` 选择策略：

- `auto`：支持 hook 的厂商默认值。收到合法 hook 后以 hook 为准，否则使用终端启发式。
- `off`：不注入 hook relay 环境，只使用终端启发式。
- `required`：只使用 hook。启动后 5 秒内没有收到合法 hook 时停止该会话。

不支持 hook 的适配器默认使用 `off`，且不能选择 `required`。

厂商配置格式见
[Claude Code hooks](https://code.claude.com/docs/en/hooks) 和
[Codex hooks](https://developers.openai.com/codex/hooks)。

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
