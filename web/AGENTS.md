# AGENTS.md — web

## 职责

**Drove Web 控制台前端**（TS + React + Vite），通过 daemon 的 REST / WebSocket
展示 agent 状态，并支持启动、停止、实时终端和精确回放。

## 当前实现

| 模块 | 职责 |
|---|---|
| `src/api/types.ts` | 与 daemon JSON 契约对齐的类型（**契约唯一事实来源在 Go 端，改动需两侧同步**） |
| `src/api/client.ts` | REST 客户端（list / start / stop / replay / timeline / frame） |
| `src/api/parsing.ts` | REST 与 WebSocket 共用的严格边界解析原语 |
| `src/api/replayParsing.ts` | timeline、frame 与 output expiry 契约解析 |
| `src/terminal/recordingBoundary.ts` | terminal DTO 到 bigint 录制领域值的单向转换 |
| `src/terminal/sessionTape.ts` | 浏览器内存中的精确 origin 前缀与时间戳关联 |
| `src/terminal/xtermAdapter.ts` | controller 私有的 xterm 构造、挂载、写入、测量与释放封装 |
| `src/terminal/sessionController.ts` | live/replay xterm、双流游标、重连、输入、resize、seek 与 teardown 的唯一所有者 |
| `src/ws/eventStream.ts` | WebSocket 事件流与输入 ack/error 关联：自动重连、超时、连接状态回调、幂等关闭 |
| `src/ws/terminalStream.ts` | `drove.v2` terminal 客户端：严格解码 raw/event/resize/snapshot，消费成功后推进 cursor |
| `src/hooks/useAgentEvents.ts` | React hook：订阅事件流 + 本地投影（`latestAgentState`） |
| `src/hooks/useAgentTerminal.ts` | controller 的 external-store React 生命周期适配器 |
| `src/navigation.ts` | `?agent=<id>` 查询导航与浏览器 Back |
| `src/components/AgentDetailPage.tsx` | 单会话 header、连接状态与终端详情布局 |
| `src/components/TerminalViewport.tsx` | live/replay xterm.js 容器与终端状态 |
| `src/components/PlaybackRail.tsx` | 状态 spans、Blocked markers、seek 与播放控制 |
| `src/App.tsx` | fleet/detail 布局、5s 列表轮询和 v1 实时事件面板 |
| `src/auth/login.ts` | 启动前从 `/login#code` 同源兑换 HttpOnly cookie 并清除 fragment |
| `src/styles.css` | 无框架的生产控制台样式与 320px+ 响应式布局 |

## 关键设计

- **fleet 与详情分流**：fleet 使用 v1 事件流做本地状态投影；详情页使用 v2 raw 和
  events 双流。timeline 与 frame 来自 REST。
- **精确回放只认 origin tape**：`SessionTape` 从 origin 保留连续的 output 和
  resize，并用事件 sequence 关联时间。frame 只做 seek preview，snapshot 只做
  live preview。
- **原始输出摘要**：`output.chunk` 只显示 offset 和解码长度；EventLog 不渲染
  `data_b64`。
- **本地投影仅是视图**：前端从事件流推导的状态只是展示用，权威状态永远以 daemon 为准（刷新列表纠正）。
- **本地录制有界**：tape 达到 64 MiB 后冻结 exact frontier，但 live xterm.js
  继续更新。origin prefix 不淘汰，过期附件通过 `OutputExpiredError` 进入明确状态。
- **工作目录兼容旧事件**：详情 header 显示 `AgentResource.dir`；旧 creation 事件
  缺少该字段时显示不可用，不从浏览器当前目录猜测。
- **Dev 代理**：`vite.config.ts` 将 `/api`、`/ws` 代理到 loopback daemon，并在每次 HTTP 请求与 WebSocket upgrade 时从 DataDir 读取控制令牌注入 Bearer 认证。
- **生产托管**：Vite 输出到 `internal/webui/dist`，由 Go embed 打包进 daemon；生产
  浏览器请求只使用同源 cookie，不读取控制令牌。

## 约束

- 禁止在组件中直接 import daemon 内部结构；类型一律经 `src/api/types.ts`。
- 事件流消费必须走 `EventStream` 类（含重连与清理）；不得在组件内手写裸 WebSocket。
- 终端订阅必须走 `TerminalStream`；v2 sequence/offset 始终使用规范十进制字符串，
  不得转成 JavaScript `number`。
- timeline/frame 响应必须从 `unknown` 严格解析；浏览器领域中的 sequence/offset
  使用 `bigint`。
- `Frame` 与 `Snapshot` 只能显示预览，不得写入 `SessionTape` 或充当 xterm 恢复状态。
- `TerminalSessionController` 是 xterm.js、游标、输入、resize、重连和回放的唯一
  所有者。React 组件不得持有这些可变对象。
- 组件卸载必须清理订阅、observer、timer、fetch 和两个 xterm.js 实例。
- 新增依赖需说明用途；样式使用单一全局 CSS，不引入 UI 框架。

## 常用命令

```bash
npm install        # 安装依赖
npm run dev        # 本地开发（需 daemon 已运行）
npm run test -- --run
npm run typecheck  # TS 严格检查
npm run build      # 更新 ../internal/webui/dist/
```
