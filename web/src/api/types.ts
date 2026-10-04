/**
 * 与 Drove daemon JSON 契约一一对应的类型定义。
 *
 * 注意：本文件的字段名必须与 Go 端 `encoding/json` 标签保持一致，
 * 任何一侧的增删都要同步这里（契约的唯一事实来源在 Go 端）。
 */

/** agent 状态（与 internal/agent.State 对齐）。 */
export type AgentState =
  | 'pending'
  | 'starting'
  | 'working'
  | 'blocked'
  | 'done'
  | 'idle'
  | 'stopped'

/** agent 运行模式（与 internal/agent.RunMode 对齐）。 */
export type RunMode = 'interactive' | 'oneshot'

/** 会话级 hook 策略（与 internal/agent.HookPolicy 对齐）。 */
export type HookPolicy = 'off' | 'auto' | 'required'

/** 当前观测到的 hook 状态（与 internal/detect.HookStatus 对齐）。 */
export type HookStatus =
  | 'off'
  | 'awaiting_hook'
  | 'fallback'
  | 'hook_active'
  | 'required_failed'
  | 'detached'

/** 状态迁移的脱敏证据。 */
export interface StateEvidence {
  source: 'session' | 'process' | 'hook' | 'heuristic' | 'timer' | 'recovery'
  event: string
  confidence: number
  delivery_id?: string
}

/** 会话状态视图（Go: session.Status）。 */
export interface AgentStatus {
  agent_id: string
  name: string
  vendor: string
  mode: RunMode
  state: AgentState
  pid?: number
  created_at: string
  updated_at: string
  last_error?: string
  hook_policy?: HookPolicy
  hook_status?: HookStatus
  last_transition?: StateEvidence
}

/** 启动会话请求（Go: session.StartRequest）。 */
export interface StartRequest {
  vendor: string
  name?: string
  command?: string
  args?: string[]
  dir?: string
  mode?: RunMode
  hooks?: HookPolicy
}

/** 事件类型（Go: event.Type）。 */
export type EventType =
  | 'state_changed'
  | 'output'
  | 'output.chunk'
  | 'error'
  | 'session_lifecycle'
  | 'agent.input'
  | 'agent.signal'
  | 'agent.resized'
  | 'agent.attachment'

/** 实时事件（Go: event.Event）。 */
export interface Event {
  seq: number
  timestamp: string
  type: EventType
  agent_id?: string
  session_id?: string
  from?: string
  to?: string
  reason?: string
  payload?: string
}

/** 回放事件行（Go: store.EventRow，注意字段名无下划线）。 */
export interface EventRow {
  Seq: number
  Timestamp: string
  Type: string
  SessionID: string
  AgentID: string
  From: string
  To: string
  Reason: string
  Payload: string
}

/** daemon 统一错误响应：{"error": "..."}。 */
export interface ErrorResponse {
  error: string
}

/** WebSocket 输入请求（Go: api.webSocketInput）。 */
export interface WebSocketInput {
  version: 1
  type: 'input'
  request_id: string
  agent_id: string
  data: string
}

/** WebSocket 输入成功响应（Go: api.webSocketAck）。 */
export interface WebSocketAck {
  version: 1
  type: 'ack'
  request_id: string
  bytes: number
}

/** WebSocket 输入失败响应（Go: api.webSocketError）。 */
export interface WebSocketError {
  version: 1
  type: 'error'
  request_id?: string
  code: string
  message: string
}

/** v2 序号与偏移使用规范十进制字符串，避免浏览器数值精度损失。 */
export type DecimalString = string & { readonly __brand: 'DecimalString' }

/** v2 录制游标。 */
export interface TerminalCursor {
  seq: DecimalString
  next_offset: DecimalString
}

/** v2 terminal stream 模式。 */
export type TerminalMode = 'raw' | 'events' | 'snapshot'

/** v2 event 模式使用的十进制安全事件 envelope。 */
export interface TerminalEventEnvelope {
  seq: DecimalString
  timestamp: string
  type: string
  agent_id?: string
  session_id?: string
  from?: string
  to?: string
  reason?: string
  payload?: string
}

/** v2 raw 二进制输出帧解码结果。 */
export interface TerminalOutput {
  kind: 'output'
  agent_id: string
  seq: DecimalString
  offset: DecimalString
  data: Uint8Array
  historical: boolean
  cursor: TerminalCursor
}

/** v2 持久化 resize 消息。 */
export interface TerminalResize {
  kind: 'resized'
  agent_id: string
  seq: DecimalString
  rows: number
  columns: number
  output_offset: DecimalString
  cursor: TerminalCursor
  historical: boolean
}

/** v2 event 消息。 */
export interface TerminalEvent {
  kind: 'event'
  agent_id: string
  event: TerminalEventEnvelope
  cursor: TerminalCursor
  historical: boolean
}

/** v2 live-only screen snapshot。 */
export interface TerminalSnapshot {
  kind: 'snapshot'
  agent_id: string
  cursor: TerminalCursor
  rows: number
  columns: number
  lines: string[]
  truncated: boolean
  restorable: false
  captured_at: string
}

/** v2 历史追平标记。 */
export interface TerminalCaughtUp {
  kind: 'caught_up'
  agent_id: string
  mode: 'raw' | 'events'
  cursor: TerminalCursor
}

/** v2 可交给消费者应用的消息。 */
export type TerminalMessage =
  | TerminalOutput
  | TerminalResize
  | TerminalEvent
  | TerminalSnapshot
  | TerminalCaughtUp

/** WebSocket 连接的连接状态。 */
export type ConnectionState = 'connecting' | 'open' | 'closed'
