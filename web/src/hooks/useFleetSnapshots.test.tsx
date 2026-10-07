import { act, create } from 'react-test-renderer'
import type { ReactTestRenderer } from 'react-test-renderer'
import { describe, expect, it } from 'vitest'
import type { TerminalSnapshot } from '../api/types'
import type {
  FleetSnapshotStream,
  FleetSnapshotStreamFactory,
  FleetSnapshotView,
} from '../fleet/fleetSnapshotController'
import {
  parseTerminalTextMessage,
  type TerminalStreamOptions,
  type TerminalSubscription,
} from '../ws/terminalStream'
import { useFleetSnapshots } from './useFleetSnapshots'

describe('useFleetSnapshots', () => {
  it('pauses the stream while preserving snapshots for fleet return', async () => {
    const streams: FakeSnapshotStream[] = []
    const createStream: FleetSnapshotStreamFactory = (options) => {
      const stream = new FakeSnapshotStream(options)
      streams.push(stream)
      return stream
    }
    let view: FleetSnapshotView | undefined

    function Probe(props: { readonly enabled: boolean }): null {
      view = useFleetSnapshots(['agent-1'], {
        createStream,
        enabled: props.enabled,
      })
      return null
    }

    const mounted: { current?: ReactTestRenderer } = {}
    await act(async () => {
      mounted.current = create(<Probe enabled />)
    })
    await expectSubscriptions(streams, 1)

    act(() => {
      streams[0]?.emit(snapshotFixture())
    })
    expect(requireView(view).byAgentID.get('agent-1')?.lines).toEqual([
      'approval prompt',
    ])

    const renderer = mounted.current
    if (renderer === undefined) throw new Error('expected mounted hook probe')
    await act(async () => {
      renderer.update(<Probe enabled={false} />)
    })
    expect(streams[0]?.closeCalls).toBe(1)
    expect(requireView(view).byAgentID.get('agent-1')?.lines).toEqual([
      'approval prompt',
    ])

    await act(async () => {
      renderer.update(<Probe enabled />)
    })
    await expectSubscriptions(streams, 2)
    expect(requireView(view).byAgentID.get('agent-1')?.lines).toEqual([
      'approval prompt',
    ])

    act(() => {
      renderer.unmount()
    })
    expect(streams[1]?.closeCalls).toBe(1)
  })
})

class FakeSnapshotStream implements FleetSnapshotStream {
  readonly subscribed: string[] = []
  closeCalls = 0
  private closed = false
  private readonly options: TerminalStreamOptions

  constructor(options: TerminalStreamOptions) {
    this.options = options
  }

  async connect(): Promise<void> {
    this.options.onStateChange?.('open')
  }

  async subscribe(subscription: TerminalSubscription): Promise<void> {
    this.subscribed.push(subscription.agent_id)
  }

  async unsubscribe(): Promise<void> {}

  close(): void {
    if (this.closed) return
    this.closed = true
    this.closeCalls += 1
    this.options.onStateChange?.('closed')
  }

  emit(snapshot: TerminalSnapshot): void {
    void this.options.onMessage(snapshot)
  }
}

function snapshotFixture(): TerminalSnapshot {
  const parsed = parseTerminalTextMessage(
    JSON.stringify({
      version: 2,
      type: 'snapshot',
      agent_id: 'agent-1',
      cursor: { seq: '2', next_offset: '3' },
      rows: 40,
      columns: 120,
      lines: ['approval prompt'],
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

async function expectSubscriptions(
  streams: ReadonlyArray<FakeSnapshotStream>,
  count: number,
): Promise<void> {
  await act(async () => {
    await Promise.resolve()
    await Promise.resolve()
  })
  expect(streams).toHaveLength(count)
  expect(streams[count - 1]?.subscribed).toEqual(['agent-1'])
}

function requireView(
  view: FleetSnapshotView | undefined,
): FleetSnapshotView {
  if (view === undefined) throw new Error('expected fleet snapshot view')
  return view
}
