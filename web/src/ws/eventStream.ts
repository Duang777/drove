/**
 * WebSocket 事件流客户端。
 *
 * 特性：
 * - 连接 /ws，透传 daemon 的实时事件；
 * - 断线自动重连（指数退避 500ms → 2s → 5s，上限 10s）；
 * - 连接状态变化通过 onStateChange 回调暴露（供 UI 显示）；
 * - 幂等清理：close() 可随时调用，回调不会再触发。
 */
import type {
  ConnectionState,
  Event,
  WebSocketAck,
  WebSocketError,
  WebSocketInput,
} from '../api/types'

const WS_PATH = `/ws`

interface Options {
  /** 收到事件时回调。 */
  onEvent: (ev: Event) => void
  /** 连接状态变化回调。 */
  onStateChange?: (state: ConnectionState) => void
}

const RETRY_BASE_MS = 500
const RETRY_MAX_MS = 10_000
const INPUT_TIMEOUT_MS = 10_000

interface PendingInput {
  resolve: (bytes: number) => void
  reject: (error: Error) => void
  timer: ReturnType<typeof setTimeout>
}

export class EventStream {
  private ws: WebSocket | null = null
  private closed = false
  private retryDelay = RETRY_BASE_MS
  private retryTimer: ReturnType<typeof setTimeout> | null = null
  private readonly pendingInputs = new Map<string, PendingInput>()

  private readonly onEvent: (ev: Event) => void
  private readonly onStateChange?: (state: ConnectionState) => void

  constructor(opts: Options) {
    this.onEvent = opts.onEvent
    this.onStateChange = opts.onStateChange
  }

  /** 建立（或恢复）连接。 */
  connect(): void {
    if (this.closed) return

    const proto = window.location.protocol === 'https:' ? 'wss' : 'ws'
    const url = `${proto}://${window.location.host}${WS_PATH}`
    this.setConnState('connecting')

    const ws = new WebSocket(url)
    this.ws = ws

    ws.onopen = () => {
      this.retryDelay = RETRY_BASE_MS // 成功即重置退避
      this.setConnState('open')
    }

    ws.onmessage = (msg) => {
      try {
        const raw = JSON.parse(String(msg.data)) as unknown
        if (!isRecord(raw)) return
        if (raw.type === 'hello') return
        if (isWebSocketAck(raw)) {
          this.resolveInput(raw)
          return
        }
        if (isWebSocketError(raw)) {
          this.rejectInput(raw)
          return
        }
        if (isEvent(raw)) this.onEvent(raw)
      } catch {
        // 单条消息解析失败不影响连接，丢弃即可。
      }
    }

    ws.onclose = () => {
      this.ws = null
      this.rejectPendingInputs(new Error('WebSocket closed before input was acknowledged'))
      this.setConnState('closed')
      if (!this.closed) this.scheduleRetry()
    }

    ws.onerror = () => {
      // onclose 会紧随其后处理重连；这里仅确保关闭。
      ws.close()
    }
  }

  /** 永久关闭，不再重连。 */
  close(): void {
    this.closed = true
    if (this.retryTimer) {
      clearTimeout(this.retryTimer)
      this.retryTimer = null
    }
    this.ws?.close()
    this.ws = null
    this.rejectPendingInputs(new Error('WebSocket event stream closed'))
  }

  /** 发送输入并等待同一连接返回关联 ack。 */
  sendInput(agentID: string, data: string): Promise<number> {
    const ws = this.ws
    if (!ws || ws.readyState !== WebSocket.OPEN) {
      return Promise.reject(new Error('WebSocket is not open'))
    }

    const requestID = crypto.randomUUID()
    const request: WebSocketInput = {
      version: 1,
      type: 'input',
      request_id: requestID,
      agent_id: agentID,
      data,
    }
    return new Promise<number>((resolve, reject) => {
      const timer = setTimeout(() => {
        this.pendingInputs.delete(requestID)
        reject(new Error('WebSocket input acknowledgement timed out'))
      }, INPUT_TIMEOUT_MS)
      this.pendingInputs.set(requestID, { resolve, reject, timer })
      try {
        ws.send(JSON.stringify(request))
      } catch (error) {
        clearTimeout(timer)
        this.pendingInputs.delete(requestID)
        reject(error instanceof Error ? error : new Error(String(error)))
      }
    })
  }

  private scheduleRetry(): void {
    this.retryTimer = setTimeout(() => {
      this.retryTimer = null
      this.connect()
    }, this.retryDelay)
    this.retryDelay = Math.min(this.retryDelay * 2, RETRY_MAX_MS)
  }

  private setConnState(state: ConnectionState): void {
    this.onStateChange?.(state)
  }

  private resolveInput(ack: WebSocketAck): void {
    const pending = this.pendingInputs.get(ack.request_id)
    if (!pending) return
    clearTimeout(pending.timer)
    this.pendingInputs.delete(ack.request_id)
    pending.resolve(ack.bytes)
  }

  private rejectInput(response: WebSocketError): void {
    if (!response.request_id) return
    const pending = this.pendingInputs.get(response.request_id)
    if (!pending) return
    clearTimeout(pending.timer)
    this.pendingInputs.delete(response.request_id)
    pending.reject(new Error(`${response.code}: ${response.message}`))
  }

  private rejectPendingInputs(error: Error): void {
    for (const pending of this.pendingInputs.values()) {
      clearTimeout(pending.timer)
      pending.reject(error)
    }
    this.pendingInputs.clear()
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null
}

function isWebSocketAck(
  value: Record<string, unknown>,
): value is Record<string, unknown> & WebSocketAck {
  return (
    value.version === 1 &&
    value.type === 'ack' &&
    typeof value.request_id === 'string' &&
    typeof value.bytes === 'number'
  )
}

function isWebSocketError(
  value: Record<string, unknown>,
): value is Record<string, unknown> & WebSocketError {
  return (
    value.version === 1 &&
    value.type === 'error' &&
    (value.request_id === undefined || typeof value.request_id === 'string') &&
    typeof value.code === 'string' &&
    typeof value.message === 'string'
  )
}

function isEvent(value: Record<string, unknown>): value is Record<string, unknown> & Event {
  return (
    typeof value.seq === 'number' &&
    typeof value.timestamp === 'string' &&
    typeof value.type === 'string'
  )
}
