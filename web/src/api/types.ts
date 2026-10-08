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

/** 会话级信号注入配置。 */
export type SignalInjectionMode = 'auto' | 'off'

/** 会话启动时的信号注入结果。 */
export type SignalInjectionStatus = 'off' | 'injected' | 'skipped' | 'detached'

/** 信号注入结果的稳定原因码。 */
export type SignalInjectionReason =
  | 'hook_policy_off'
  | 'configured_off'
  | 'unsupported'
  | 'relay_unavailable'
  | 'argument_conflict'
  | 'session_config'
  | 'recovered'

/** screen 状态证据的有界归因。 */
export interface ScreenAttribution {
  rule: string
  edge: 'present' | 'cleared'
  region: string
  output_offset: number
  last_output_seq: number
  evidence: string
}

/** terminal notify 状态证据的有界归因。 */
export interface TerminalAttribution {
  protocol: 'osc9'
  output_offset: number
  last_output_seq: number
}

interface StateEvidenceBase {
  event: string
  confidence: number
}

/** 状态迁移的脱敏证据。 */
export type StateEvidence =
  | (StateEvidenceBase & {
      source: 'hook'
      delivery_id: string
    })
  | (StateEvidenceBase & {
      source: 'notify'
      delivery_id: string
    })
  | (StateEvidenceBase & {
      source: 'notify'
      terminal: TerminalAttribution
    })
  | (StateEvidenceBase & {
      source: 'screen'
      screen: ScreenAttribution
    })
  | (StateEvidenceBase & {
      source:
        | 'session'
        | 'process'
        | 'heuristic'
        | 'timer'
        | 'recovery'
    })

/** 会话状态视图（Go: session.Status）。 */
export interface AgentStatus {
  agent_id: string
  name: string
  vendor: string
  dir?: string
  mode: RunMode
  state: AgentState
  state_seq: DecimalString
  pid?: number
  created_at: string
  updated_at: string
  state_since: string
  last_error?: string
  hook_policy: HookPolicy
  hook_status: HookStatus
  signal_injection: SignalInjectionMode
  signal_injection_status: SignalInjectionStatus
  signal_injection_reason: SignalInjectionReason
  last_transition?: StateEvidence
  resumable: boolean
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

/** Web Push channel availability and public browser enrollment key. */
export type WebPushAvailability =
  | { readonly kind: 'unavailable' }
  | { readonly kind: 'available'; readonly vapidPublicKey: string }

/** Notification policy exposed without provider credentials. */
export interface NotificationPolicy {
  readonly on: ReadonlyArray<'blocked'>
  readonly debounceSeconds: number
  readonly quietWhenActive: boolean
}

/** Public notification status. */
export interface NotificationStatus {
  readonly webPush: WebPushAvailability
  readonly ntfyAvailable: boolean
  readonly policy: NotificationPolicy
  readonly activeDeviceCount: number
}

/** Public push target metadata. Endpoint and key material never cross this API. */
export interface PushDevice {
  readonly id: string
  readonly deviceName: string
  readonly createdAt: Timestamp
}

/** Browser subscription material accepted by the daemon. */
export interface PushSubscriptionInput {
  readonly endpoint: string
  readonly p256dh: string
  readonly auth: string
  readonly deviceName: string
}

/** 事件类型（Go: event.Type）。 */
export type EventType =
  | 'state_changed'
  | 'output'
  | 'output.chunk'
  | 'error'
  | 'session_lifecycle'
  | 'agent.input'
  | 'agent.action'
  | 'agent.signal'
  | 'agent.resized'
  | 'agent.attachment'
  | 'agent.resumed'

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
  timestamp: Timestamp
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
  captured_at: Timestamp
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

/** 已校验的 RFC3339 时间戳及其毫秒值。 */
export interface Timestamp {
  readonly iso: string
  readonly epochMillis: number
}

/** 浏览器领域内的录制游标。 */
export interface RecordingCursor {
  readonly seq: bigint
  readonly nextOffset: bigint
}

/** 半开终端输出字节范围。 */
export interface OutputRange {
  readonly start: bigint
  readonly end: bigint
}

/** timeline 中由状态事件派生的半开区间。 */
export interface TimelineStateSpan {
  readonly state: AgentState
  readonly start: RecordingCursor
  readonly end?: RecordingCursor
  readonly startAt: Timestamp
  readonly endAt?: Timestamp
  readonly durationMillis?: number
  readonly source?: string
  readonly rule?: string
  readonly reason?: string
}

/** 一次进入 Blocked 状态的权威索引。 */
export interface BlockedOccurrence {
  readonly number: number
  readonly span: TimelineStateSpan
  readonly jump: RecordingCursor
  readonly frameAvailable: boolean
}

/** timeline 报告的输出保留情况。 */
export interface OutputCoverage {
  readonly range: OutputRange
  readonly retained: ReadonlyArray<OutputRange>
  readonly missing: ReadonlyArray<OutputRange>
}

/** 从一个不可变会话前缀投影出的权威时间线。 */
export interface TerminalTimeline {
  readonly sessionID: string
  readonly agentID: string
  readonly captured: RecordingCursor
  readonly capturedAt: Timestamp
  readonly durationMillis: number
  readonly output: OutputCoverage
  readonly spans: ReadonlyArray<TimelineStateSpan>
  readonly blocked: ReadonlyArray<BlockedOccurrence>
}

/** Frame 仅供可见预览，不能作为精确回放状态。 */
export interface TerminalFramePreview {
  readonly kind: 'frame_preview'
  readonly sessionID: string
  readonly cursor: RecordingCursor
  readonly rows: number
  readonly columns: number
  readonly lines: ReadonlyArray<string>
  readonly truncated: boolean
  readonly fidelity: 'exact_origin_replay'
  readonly restorable: false
}

/** Frame REST 查询只允许一个 selector。 */
export type FrameSelector =
  | { readonly kind: 'sequence'; readonly seq: bigint }
  | { readonly kind: 'timestamp'; readonly at: Timestamp }
  | { readonly kind: 'offset'; readonly offset: bigint }
