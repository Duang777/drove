import { afterEach, describe, expect, it, vi } from 'vitest'
import type { TerminalSnapshot } from '../api/types'
import {
  parseTerminalTextMessage,
  type TerminalStreamOptions,
  type TerminalSubscription,
} from '../ws/terminalStream'
import {
  FleetSnapshotController,
  type FleetSnapshotStream,
} from './fleetSnapshotController'

afterEach(() => {
  vi.useRealTimers()
})

describe('FleetSnapshotController', () => {
  it('uses one connection for all live agents and reconciles subscriptions', async () => {
    const streams: FakeSnapshotStream[] = []
    const controller = new FleetSnapshotController({
      createStream: (options) => {
        const stream = new FakeSnapshotStream(options)
        streams.push(stream)
        return stream
      },
    })

    controller.setAgentIDs(['agent-2', 'agent-1', 'agent-2'])
    controller.start()

    await vi.waitFor(() => {
      expect(streams).toHaveLength(1)
      expect(streams[0]?.subscribed).toEqual(['agent-1', 'agent-2'])
    })

    streams[0]?.emit(snapshotFixture('agent-1', ['first', 'prompt>']))
    expect(controller.getSnapshot().byAgentID.get('agent-1')?.lines).toEqual([
      'first',
      'prompt>',
    ])

    controller.setAgentIDs(['agent-2', 'agent-3'])
    await vi.waitFor(() => {
      expect(streams[0]?.unsubscribed).toEqual(['agent-1'])
      expect(streams[0]?.subscribed).toEqual([
        'agent-1',
        'agent-2',
        'agent-3',
      ])
    })
    expect(controller.getSnapshot().byAgentID.has('agent-1')).toBe(false)

    controller.setAgentIDs([])
    expect(streams[0]?.closeCalls).toBe(1)
    expect(controller.getSnapshot().connection).toBe('closed')

    controller.dispose()
    expect(streams[0]?.closeCalls).toBe(1)
  })

  it('reconnects one stream and restores the current subscription set', async () => {
    vi.useFakeTimers()
    const streams: FakeSnapshotStream[] = []
    const controller = new FleetSnapshotController({
      reconnectDelayMillis: 500,
      createStream: (options) => {
        const stream = new FakeSnapshotStream(options)
        streams.push(stream)
        return stream
      },
    })

    controller.setAgentIDs(['agent-1', 'agent-2'])
    controller.start()
    await flushPromises()
    expect(streams[0]?.subscribed).toEqual(['agent-1', 'agent-2'])

    streams[0]?.disconnect()
    await vi.advanceTimersByTimeAsync(500)
    await flushPromises()

    expect(streams).toHaveLength(2)
    expect(streams[1]?.subscribed).toEqual(['agent-1', 'agent-2'])

    controller.dispose()
    await vi.advanceTimersByTimeAsync(1_000)
    expect(streams).toHaveLength(2)
  })

  it('reconnects when subscription reconciliation fails', async () => {
    vi.useFakeTimers()
    const streams: FakeSnapshotStream[] = []
    const errors: Error[] = []
    const controller = new FleetSnapshotController({
      reconnectDelayMillis: 500,
      onError: (error) => errors.push(error),
      createStream: (options) => {
        const stream = new FakeSnapshotStream(
          options,
          streams.length === 0 ? 'agent-2' : undefined,
        )
        streams.push(stream)
        return stream
      },
    })

    controller.setAgentIDs(['agent-1', 'agent-2'])
    controller.start()
    await flushPromises()

    expect(errors.map((error) => error.message)).toEqual([
      'subscribe failed for agent-2',
    ])
    expect(streams[0]?.closeCalls).toBe(1)
    expect(controller.getSnapshot().connection).toBe('closed')

    await vi.advanceTimersByTimeAsync(500)
    await flushPromises()
    expect(streams).toHaveLength(2)
    expect(streams[1]?.subscribed).toEqual(['agent-1', 'agent-2'])

    controller.dispose()
  })
})

class FakeSnapshotStream implements FleetSnapshotStream {
  readonly subscribed: string[] = []
  readonly unsubscribed: string[] = []
  closeCalls = 0
  private closed = false
  private readonly options: TerminalStreamOptions
  private readonly failedSubscriptionAgentID?: string

  constructor(
    options: TerminalStreamOptions,
    failedSubscriptionAgentID?: string,
  ) {
    this.options = options
    this.failedSubscriptionAgentID = failedSubscriptionAgentID
  }

  async connect(): Promise<void> {
    this.options.onStateChange?.('connecting')
    this.options.onStateChange?.('open')
  }

  async subscribe(subscription: TerminalSubscription): Promise<void> {
    if (subscription.mode !== 'snapshot') {
      throw new Error('expected snapshot subscription')
    }
    if (subscription.agent_id === this.failedSubscriptionAgentID) {
      throw new Error(`subscribe failed for ${subscription.agent_id}`)
    }
    this.subscribed.push(subscription.agent_id)
  }

  async unsubscribe(agentID: string): Promise<void> {
    this.unsubscribed.push(agentID)
  }

  close(): void {
    if (this.closed) return
    this.closed = true
    this.closeCalls += 1
    this.options.onStateChange?.('closed')
  }

  emit(snapshot: TerminalSnapshot): void {
    void this.options.onMessage(snapshot)
  }

  disconnect(): void {
    this.options.onStateChange?.('closed')
  }
}

function snapshotFixture(
  agentID: string,
  lines: readonly string[],
): TerminalSnapshot {
  const parsed = parseTerminalTextMessage(
    JSON.stringify({
      version: 2,
      type: 'snapshot',
      agent_id: agentID,
      cursor: { seq: '2', next_offset: '3' },
      rows: 40,
      columns: 120,
      lines,
      truncated: false,
      restorable: false,
      captured_at: '2026-10-04T12:00:00Z',
    }),
  )
  if (parsed.kind !== 'stream' || parsed.message.kind !== 'snapshot') {
    throw new Error('expected parsed snapshot')
  }
  return parsed.message
}

async function flushPromises(): Promise<void> {
  for (let index = 0; index < 8; index += 1) {
    await Promise.resolve()
  }
}
