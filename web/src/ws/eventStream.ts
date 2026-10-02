/**
 * WebSocket 事件流客户端。
 *
 * 特性：
 * - 连接 /ws，透传 daemon 的实时事件；
 * - 断线自动重连（指数退避 500ms → 2s → 5s，上限 10s）；
 * - 连接状态变化通过 onStateChange 回调暴露（供 UI 显示）；
 * - 幂等清理：close() 可随时调用，回调不会再触发。
 */
import type { ConnectionState, Event } from '../api/types'

const WS_PATH = `/ws`

interface Options {
  /** 收到事件时回调。 */
  onEvent: (ev: Event) => void
  /** 连接状态变化回调。 */
  onStateChange?: (state: ConnectionState) => void
}

const RETRY_BASE_MS = 500
const RETRY_MAX_MS = 10_000

export class EventStream {
  private ws: WebSocket | null = null
  private closed = false
  private retryDelay = RETRY_BASE_MS
  private retryTimer: ReturnType<typeof setTimeout> | null = null

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
        if (
          typeof raw === 'object' &&
          raw !== null &&
          (raw as { type?: unknown }).type === 'hello'
        ) {
          return // 握手消息，忽略
        }
        this.onEvent(raw as Event)
      } catch {
        // 单条消息解析失败不影响连接，丢弃即可。
      }
    }

    ws.onclose = () => {
      this.ws = null
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
}
