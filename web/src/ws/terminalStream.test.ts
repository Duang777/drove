import { afterEach, describe, expect, it, vi } from 'vitest'
import {
  TerminalStream,
  parseTerminalTextMessage,
} from './terminalStream'

afterEach(() => {
  FakeWebSocket.instances.length = 0
  vi.unstubAllGlobals()
})

describe('terminal v2 text parsing', () => {
  it('parses event timestamps before exposing the message', () => {
    const parsed = parseTerminalTextMessage(
      JSON.stringify({
        version: 2,
        type: 'event',
        agent_id: 'agent-1',
        event: {
          seq: '2',
          timestamp: '2026-10-04T12:00:00.123456789Z',
          type: 'output.chunk',
        },
        cursor: { seq: '2', next_offset: '3' },
        historical: true,
      }),
    )

    expect(parsed).toEqual({
      kind: 'stream',
      message: {
        kind: 'event',
        agent_id: 'agent-1',
        event: {
          seq: '2',
          timestamp: {
            iso: '2026-10-04T12:00:00.123456789Z',
            epochMillis: 1_791_115_200_123,
          },
          type: 'output.chunk',
        },
        cursor: { seq: '2', next_offset: '3' },
        historical: true,
      },
    })
  })

  it.each([
    ['noncanonical sequence', '02', '2026-10-04T12:00:00Z'],
    ['invalid timestamp', '2', '2026-02-30T12:00:00Z'],
  ])('rejects %s', (_name, seq, timestamp) => {
    expect(() =>
      parseTerminalTextMessage(
        JSON.stringify({
          version: 2,
          type: 'event',
          agent_id: 'agent-1',
          event: { seq, timestamp, type: 'output.chunk' },
          cursor: { seq, next_offset: '3' },
          historical: true,
        }),
      ),
    ).toThrow()
  })

  it('rejects restorable snapshots', () => {
    expect(() =>
      parseTerminalTextMessage(
        JSON.stringify({
          version: 2,
          type: 'snapshot',
          agent_id: 'agent-1',
          cursor: { seq: '2', next_offset: '3' },
          rows: 40,
          columns: 120,
          lines: ['hello'],
          truncated: false,
          restorable: true,
          captured_at: '2026-10-04T12:00:00Z',
        }),
      ),
    ).toThrow(/non-restorable/)
  })
})

describe('TerminalStream delivery', () => {
  it('preserves read-only attachment intent and advances only an applied cursor', async () => {
    vi.stubGlobal('window', {
      location: { protocol: 'http:', host: 'localhost:7373' },
    })
    vi.stubGlobal('WebSocket', FakeWebSocket)
    const applied = deferred<void>()
    const stream = new TerminalStream({
      onMessage: async () => applied.promise,
    })

    const connecting = stream.connect()
    const socket = FakeWebSocket.instances[0]
    if (socket === undefined) throw new Error('expected terminal socket')
    socket.receive(JSON.stringify({ version: 2, type: 'hello' }))
    await connecting

    const subscribing = stream.subscribe({
      agent_id: 'agent-1',
      mode: 'raw',
      writable: false,
    })
    const command = parseObject(socket.sent[0])
    expect(command).toMatchObject({
      version: 2,
      type: 'subscribe',
      agent_id: 'agent-1',
      mode: 'raw',
      writable: false,
    })
    const requestID = command.request_id
    if (typeof requestID !== 'string') {
      throw new Error('expected subscribe request ID')
    }
    socket.receive(
      JSON.stringify({
        version: 2,
        type: 'subscribed',
        request_id: requestID,
        agent_id: 'agent-1',
        mode: 'raw',
        cursor: { seq: '0', next_offset: '0' },
      }),
    )
    await subscribing
    expect(stream.cursor('agent-1', 'raw')).toBeUndefined()

    socket.receive(
      JSON.stringify({
        version: 2,
        type: 'caught_up',
        agent_id: 'agent-1',
        mode: 'raw',
        cursor: { seq: '0', next_offset: '0' },
      }),
    )
    await Promise.resolve()
    expect(stream.cursor('agent-1', 'raw')).toBeUndefined()

    applied.resolve()
    await vi.waitFor(() => {
      expect(stream.cursor('agent-1', 'raw')).toEqual({
        seq: '0',
        next_offset: '0',
      })
    })
    stream.close()
  })
})

class FakeWebSocket {
  static readonly OPEN = 1
  static readonly instances: FakeWebSocket[] = []

  readonly sent: string[] = []
  readyState = FakeWebSocket.OPEN
  binaryType = 'blob'
  onmessage: ((event: { readonly data: unknown }) => void) | null = null
  onerror: (() => void) | null = null
  onclose: (() => void) | null = null

  constructor() {
    FakeWebSocket.instances.push(this)
  }

  send(data: unknown): void {
    if (typeof data !== 'string') {
      throw new Error('expected terminal text command')
    }
    this.sent.push(data)
  }

  close(): void {
    this.readyState = 3
    this.onclose?.()
  }

  receive(data: unknown): void {
    this.onmessage?.({ data })
  }
}

interface Deferred<T> {
  readonly promise: Promise<T>
  readonly resolve: (value: T) => void
}

function deferred<T>(): Deferred<T> {
  let resolvePromise: ((value: T) => void) | undefined
  const promise = new Promise<T>((resolve) => {
    resolvePromise = resolve
  })
  return {
    promise,
    resolve: (value) => resolvePromise?.(value),
  }
}

function parseObject(value: string | undefined): Record<string, unknown> {
  if (value === undefined) throw new Error('expected command')
  const parsed: unknown = JSON.parse(value)
  if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) {
    throw new Error('expected command object')
  }
  return Object.fromEntries(Object.entries(parsed))
}
