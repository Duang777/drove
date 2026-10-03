# AGENTS.md — web

## 职责

**Drove Web 控制台前端**（TS + React + Vite），通过 daemon 的 REST / WebSocket 展示 agent 状态并支持交互（启动 / 停止 / 事件回放）。

## 当前实现（MVP 骨架）

| 模块 | 职责 |
|---|---|
| `src/api/types.ts` | 与 daemon JSON 契约对齐的类型（**契约唯一事实来源在 Go 端，改动需两侧同步**） |
| `src/api/client.ts` | REST 客户端（list / start / stop / replay），纯 JSON 透传 |
| `src/ws/eventStream.ts` | WebSocket 事件流与输入 ack/error 关联：自动重连、超时、连接状态回调、幂等关闭 |
| `src/hooks/useAgentEvents.ts` | React hook：订阅事件流 + 本地投影（`latestAgentState`） |
| `src/components/` | AgentList / AgentCard / StatusBadge / EventLog |
| `src/App.tsx` | 布局 + 5s 轮询刷新 agent 列表 + 实时事件面板 |

## 关键设计

- **实时与回放双轨**：WebSocket 承载实时事件；回放走 REST（`/api/v1/agents/{id}/events`），两者展示在同一个 EventLog。
- **本地投影仅是视图**：前端从事件流推导的状态只是展示用，权威状态永远以 daemon 为准（刷新列表纠正）。
- **Dev 代理**：`vite.config.ts` 将 `/api`、`/ws` 代理到 loopback daemon，并在每次 HTTP 请求与 WebSocket upgrade 时从 DataDir 读取控制令牌注入 Bearer 认证。

## 约束

- 禁止在组件中直接 import daemon 内部结构；类型一律经 `src/api/types.ts`。
- 事件流消费必须走 `EventStream` 类（含重连与清理）；不得在组件内手写裸 WebSocket。
- 组件卸载必须清理订阅（`useAgentEvents` 已内置）。
- 新增依赖需说明用途；样式沿用现有类名风格，不引入 UI 框架（保持骨架轻量）。

## 常用命令

```bash
npm install        # 安装依赖
npm run dev        # 本地开发（需 daemon 已运行）
npm run typecheck  # TS 严格检查
npm run build      # 产出 dist/
```
