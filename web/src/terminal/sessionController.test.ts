import { describe, expect, it, vi } from 'vitest'
import type {
  OutputRange,
  TerminalCaughtUp,
  TerminalEvent,
  TerminalFramePreview,
  TerminalOutput,
  TerminalResize,
  TerminalTimeline,
  Timestamp,
} from '../api/types'
import { decimalString } from '../ws/terminalStream'
import type {
  TerminalStreamOptions,
  TerminalSubscription,
} from '../ws/terminalStream'
import { SessionTape } from './sessionTape'
import {
  TerminalSessionController,
  type TerminalControllerDependencies,
} from './sessionController'
import type {
  TerminalAdapter,
  TerminalAdapterOptions,
  TerminalDimensions,
  TerminalDisposable,
} from './xtermAdapter'

describe('TerminalSessionController', () => {
  it('awaits live output and resumes raw and event streams from separate applied cursors', async () => {
    const fixture = controllerFixture()
    await fixture.controller.start()
    await fixture.controller.start()
    expect(fixture.streams).toHaveLength(1)
    const firstStream = fixture.streams[0]
    const live = fixture.terminals[0]
    if (firstStream === undefined || live === undefined) {
      throw new Error('expected live terminal and initial stream')
    }

    const write = live.blockNextWrite()
    const delivered = firstStream.emit(outputMessage(2n, 0n, [0x68, 0x69]))
    await Promise.resolve()
    expect(live.writes).toEqual([Uint8Array.from([0x68, 0x69])])
    expect(await promiseState(delivered)).toBe('pending')

    write.resolve()
    await delivered
    await firstStream.emit(caughtUpMessage('raw', 3n, 2n))
    firstStream.fail(new Error('connection lost'))

    await vi.waitFor(() => {
      expect(fixture.streams).toHaveLength(2)
    })
    const resumed = fixture.streams[1]
    if (resumed === undefined) throw new Error('expected resumed stream')
    expect(resumed.subscriptions).toEqual([
      {
        agent_id: 'agent-1',
        mode: 'raw',
        writable: true,
        viewport: { rows: 40, columns: 120 },
        start: {
          kind: 'cursor',
          cursor: { seq: decimalString(3n), next_offset: decimalString(2n) },
        },
      },
      {
        agent_id: 'agent-1',
        mode: 'events',
      },
    ])
    expect(
      fixture.terminals.filter(
        (terminal) => terminal.options.kind === 'live',
      ),
    ).toHaveLength(1)

    await firstStream.emit(outputMessage(3n, 2n, [0x21]))
    expect(live.writes).toHaveLength(1)
  })

  it('rejects a raw caught_up cursor that skips unapplied output', async () => {
    const fixture = controllerFixture()
    await fixture.controller.start()
    const stream = fixture.streams[0]
    if (stream === undefined) throw new Error('expected initial stream')

    await stream.emit(outputMessage(2n, 0n, [0x68, 0x69]))
    await expect(
      stream.emit(caughtUpMessage('raw', 3n, 3n)),
    ).rejects.toThrow('does not match locally applied output')
  })

  it('enables serialized UTF-8 input only after raw caught_up and applies resize only from durable records', async () => {
    const fixture = controllerFixture()
    const live = fixture.terminals[0]
    if (live === undefined) throw new Error('expected live terminal')
    live.proposedDimensions = { rows: 50, columns: 100 }
    await fixture.controller.start()
    const stream = fixture.streams[0]
    if (stream === undefined) throw new Error('expected initial stream')

    live.emitInput('ignored')
    await Promise.resolve()
    expect(stream.inputCalls).toEqual([])

    const firstResize = stream.blockNextResize()
    await stream.emit(caughtUpMessage('raw', 0n, 0n))
    expect(fixture.controller.getSnapshot().inputEnabled).toBe(true)
    expect(stream.resizeCalls).toEqual([
      { agentID: 'agent-1', rows: 50, columns: 100 },
    ])
    expect(live.resizes).toEqual([])

    const firstAck = stream.blockNextInput()
    const secondAck = stream.blockNextInput()
    live.emitInput(`${'a'.repeat(65_535)}😀b`)
    await vi.waitFor(() => {
      expect(stream.inputCalls).toHaveLength(1)
    })
    expect(utf8Length(stream.inputCalls[0]?.data ?? '')).toBeLessThanOrEqual(
      64 * 1024,
    )

    firstAck.resolve(65_535)
    await vi.waitFor(() => {
      expect(stream.inputCalls).toHaveLength(2)
    })
    expect(stream.inputCalls[1]?.data).toBe('😀b')
    expect(utf8Length(stream.inputCalls[1]?.data ?? '')).toBe(5)
    secondAck.resolve(5)

    live.proposedDimensions = { rows: 60, columns: 110 }
    await stream.emit(resizeMessage(1n, 0n, 50, 100))
    expect(live.resizes).toEqual([{ rows: 50, columns: 100 }])
    expect(stream.resizeCalls).toHaveLength(1)
    firstResize.resolve()
    await vi.waitFor(() => {
      expect(stream.resizeCalls).toEqual([
        { agentID: 'agent-1', rows: 50, columns: 100 },
        { agentID: 'agent-1', rows: 60, columns: 110 },
      ])
    })
  })

  it('continues live writes after the local replay prefix reaches its byte limit', async () => {
    const fixture = controllerFixture({
      createTape: () => new SessionTape({ byteLimit: 2 }),
    })
    await fixture.controller.start()
    const stream = fixture.streams[0]
    const live = fixture.terminals[0]
    if (stream === undefined || live === undefined) {
      throw new Error('expected initial stream and terminal')
    }

    await stream.emit(outputMessage(1n, 0n, [1, 2]))
    await stream.emit(eventMessage(1n, 2n, 1_000))
    await stream.emit(outputMessage(2n, 2n, [3]))
    await stream.emit(eventMessage(2n, 3n, 2_000))
    expect(live.writes).toEqual([
      Uint8Array.from([1, 2]),
      Uint8Array.from([3]),
    ])

    fixture.controller.dispatch({ kind: 'commit_seek', atMillis: 2_000 })
    await vi.waitFor(() => {
      expect(fixture.controller.getSnapshot().replay).toEqual({
        kind: 'local_limit',
        atMillis: 2_000,
        byteLimit: 2,
      })
    })
  })

  it('uses bounded reconnect backoff until a new stream subscribes', async () => {
    const streams: FakeStream[] = []
    const delays: number[] = []
    let failuresRemaining = 6
    const fixture = controllerFixture({
      createStream: (options) => {
        const stream = new FakeStream(options)
        if (failuresRemaining > 0) {
          stream.connectError = new Error('offline')
          failuresRemaining -= 1
        }
        streams.push(stream)
        return stream
      },
      sleep: async (milliseconds) => {
        delays.push(milliseconds)
      },
    })

    await fixture.controller.start()
    await vi.waitFor(() => {
      expect(streams).toHaveLength(7)
      expect(streams[6]?.subscriptions).toHaveLength(2)
    })
    expect(delays).toEqual([250, 500, 1_000, 2_000, 5_000, 5_000])
    expect(fixture.terminals).toHaveLength(1)
  })

  it('keeps reconnecting when a replacement closes during subscription setup', async () => {
    const streams: FakeStream[] = []
    const delays: number[] = []
    const fixture = controllerFixture({
      createStream: (options) => {
        const stream = new FakeStream(options)
        if (streams.length < 2) stream.closeOnSubscribeNumber = 1
        streams.push(stream)
        return stream
      },
      sleep: async (milliseconds) => {
        delays.push(milliseconds)
      },
    })

    await fixture.controller.start()
    await vi.waitFor(() => {
      expect(streams).toHaveLength(3)
      expect(streams[2]?.subscriptions).toHaveLength(2)
    })
    expect(delays).toEqual([250, 500])
    expect(fixture.controller.getSnapshot().connection).toBe('syncing')
  })

  it('rebuilds fresh 40x120 replay terminals and preserves playback timing at the selected speed', async () => {
    const sleeper = new ControlledSleeper()
    const fixture = controllerFixture({ sleep: sleeper.sleep })
    await fixture.controller.start()
    const stream = fixture.streams[0]
    const live = fixture.terminals[0]
    if (stream === undefined || live === undefined) {
      throw new Error('expected initial stream and terminal')
    }

    await stream.emit(outputMessage(1n, 0n, [0x61]))
    await stream.emit(eventMessage(1n, 1n, 1_000))
    await stream.emit(resizeMessage(2n, 1n, 50, 100))
    await stream.emit(eventMessage(2n, 1n, 2_000))
    await stream.emit(outputMessage(3n, 1n, [0x62]))
    await stream.emit(eventMessage(3n, 2n, 3_000))
    await stream.emit(caughtUpMessage('raw', 3n, 2n))
    expect(fixture.controller.getSnapshot().inputEnabled).toBe(true)

    fixture.controller.dispatch({ kind: 'commit_seek', atMillis: 2_000 })
    await vi.waitFor(() => {
      expect(fixture.controller.getSnapshot().replay.kind).toBe('ready')
    })
    expect(fixture.controller.getSnapshot().inputEnabled).toBe(false)
    const firstReplay = fixture.terminals[1]
    if (firstReplay === undefined) throw new Error('expected replay terminal')
    expect(firstReplay.options).toEqual({
      kind: 'replay',
      rows: 40,
      columns: 120,
    })
    expect(firstReplay.effects).toEqual([
      'write:97',
      'resize:50x100',
    ])

    await stream.emit(outputMessage(4n, 2n, [0x63]))
    await stream.emit(eventMessage(4n, 3n, 4_000))
    expect(live.effects).toContain('write:99')
    expect(firstReplay.effects).not.toContain('write:99')

    fixture.controller.dispatch({ kind: 'set_speed', speed: 2 })
    fixture.controller.dispatch({ kind: 'play' })
    await vi.waitFor(() => {
      expect(sleeper.requests).toHaveLength(1)
    })
    expect(sleeper.requests[0]?.milliseconds).toBe(500)
    sleeper.requests[0]?.resolve()
    await vi.waitFor(() => {
      expect(firstReplay.effects).toContain('write:98')
    })

    fixture.controller.dispatch({ kind: 'commit_seek', atMillis: 1_000 })
    await vi.waitFor(() => {
      expect(fixture.terminals).toHaveLength(3)
    })
    expect(firstReplay.disposeCalls).toBe(1)
    expect(live.disposeCalls).toBe(0)
    expect(fixture.terminals[2]?.options).toEqual({
      kind: 'replay',
      rows: 40,
      columns: 120,
    })
    fixture.controller.dispatch({ kind: 'go_live' })
    expect(fixture.controller.getSnapshot().inputEnabled).toBe(true)
    expect(live.effects).toContain('write:99')
  })

  it('disposes resources once and ignores callbacks from stale stream generations', async () => {
    const fixture = controllerFixture()
    await fixture.controller.start()
    const first = fixture.streams[0]
    const live = fixture.terminals[0]
    if (first === undefined || live === undefined) {
      throw new Error('expected initial stream and terminal')
    }
    first.fail(new Error('restart'))
    await vi.waitFor(() => {
      expect(fixture.streams).toHaveLength(2)
    })

    await first.emit(outputMessage(1n, 0n, [1]))
    expect(live.writes).toEqual([])

    fixture.controller.dispose()
    fixture.controller.dispose()
    await fixture.streams[1]?.emit(outputMessage(1n, 0n, [2]))
    expect(live.writes).toEqual([])
    expect(live.disposeCalls).toBe(1)
    expect(live.inputDisposeCalls).toBe(1)
    expect(fixture.streams[1]?.closeCalls).toBe(1)
    expect(fixture.controller.getSnapshot().connection).toBe('disposed')
  })
})

interface ControllerFixture {
  readonly controller: TerminalSessionController
  readonly terminals: FakeTerminal[]
  readonly streams: FakeStream[]
}

function controllerFixture(
  overrides: Partial<TerminalControllerDependencies> = {},
): ControllerFixture {
  const terminals: FakeTerminal[] = []
  const streams: FakeStream[] = []
  const dependencies: Partial<TerminalControllerDependencies> = {
    createTerminal: (options) => {
      const terminal = new FakeTerminal(options)
      terminals.push(terminal)
      return terminal
    },
    createStream: (options) => {
      const stream = new FakeStream(options)
      streams.push(stream)
      return stream
    },
    loadTimeline: async () => timeline(),
    loadFrame: async () => framePreview(),
    sleep: async () => {},
    yieldToBrowser: async () => {},
    createResizeObserver: () => null,
    ...overrides,
  }
  return {
    controller: new TerminalSessionController({
      agentID: 'agent-1',
      access: 'read_write',
      dependencies,
    }),
    terminals,
    streams,
  }
}

class FakeTerminal implements TerminalAdapter {
  readonly writes: Uint8Array[] = []
  readonly resizes: TerminalDimensions[] = []
  readonly effects: string[] = []
  readonly options: TerminalAdapterOptions
  proposedDimensions: TerminalDimensions | undefined = {
    rows: 40,
    columns: 120,
  }
  disposeCalls = 0
  inputDisposeCalls = 0
  private inputListener: ((data: string) => void) | null = null
  private readonly writeBlocks: Array<Deferred<void>> = []

  constructor(options: TerminalAdapterOptions) {
    this.options = options
  }

  mount(): void {}

  async write(data: Uint8Array): Promise<void> {
    this.writes.push(data.slice())
    this.effects.push(`write:${Array.from(data).join(',')}`)
    const block = this.writeBlocks.shift()
    if (block !== undefined) await block.promise
  }

  async resize(dimensions: TerminalDimensions): Promise<void> {
    this.resizes.push(dimensions)
    this.effects.push(`resize:${dimensions.rows}x${dimensions.columns}`)
  }

  onInput(listener: (data: string) => void): TerminalDisposable {
    this.inputListener = listener
    return {
      dispose: () => {
        this.inputDisposeCalls += 1
        this.inputListener = null
      },
    }
  }

  proposeDimensions(): TerminalDimensions | undefined {
    return this.proposedDimensions
  }

  focus(): void {}

  dispose(): void {
    this.disposeCalls += 1
  }

  emitInput(data: string): void {
    this.inputListener?.(data)
  }

  blockNextWrite(): Deferred<void> {
    const block = deferred<void>()
    this.writeBlocks.push(block)
    return block
  }
}

class FakeStream {
  readonly subscriptions: TerminalSubscription[] = []
  readonly inputCalls: Array<{ agentID: string; data: string }> = []
  readonly resizeCalls: Array<{
    agentID: string
    rows: number
    columns: number
  }> = []
  closeCalls = 0
  connectError: Error | null = null
  closeOnSubscribeNumber: number | null = null
  private readonly inputBlocks: Array<Deferred<number>> = []
  private readonly resizeBlocks: Array<Deferred<void>> = []
  private readonly options: TerminalStreamOptions

  constructor(options: TerminalStreamOptions) {
    this.options = options
  }

  async connect(): Promise<void> {
    if (this.connectError !== null) throw this.connectError
    this.options.onStateChange?.('open')
  }

  close(): void {
    this.closeCalls += 1
  }

  async subscribe(subscription: TerminalSubscription): Promise<void> {
    this.subscriptions.push(subscription)
    if (this.subscriptions.length === this.closeOnSubscribeNumber) {
      this.options.onStateChange?.('closed')
    }
  }

  async sendInput(agentID: string, data: string): Promise<number> {
    this.inputCalls.push({ agentID, data })
    const block = this.inputBlocks.shift()
    return block === undefined ? utf8Length(data) : block.promise
  }

  async resize(
    agentID: string,
    rows: number,
    columns: number,
  ): Promise<void> {
    this.resizeCalls.push({ agentID, rows, columns })
    const block = this.resizeBlocks.shift()
    if (block !== undefined) await block.promise
  }

  emit(message: Parameters<TerminalStreamOptions['onMessage']>[0]): Promise<void> {
    return Promise.resolve(this.options.onMessage(message))
  }

  fail(error: Error): void {
    this.options.onError?.(error)
  }

  blockNextInput(): Deferred<number> {
    const block = deferred<number>()
    this.inputBlocks.push(block)
    return block
  }

  blockNextResize(): Deferred<void> {
    const block = deferred<void>()
    this.resizeBlocks.push(block)
    return block
  }
}

class ControlledSleeper {
  readonly requests: Array<
    Deferred<void> & { readonly milliseconds: number }
  > = []

  sleep = (
    milliseconds: number,
    signal: AbortSignal,
  ): Promise<void> => {
    const request = deferred<void>()
    const entry = { ...request, milliseconds }
    this.requests.push(entry)
    signal.addEventListener(
      'abort',
      () => request.reject(new DOMException('Aborted', 'AbortError')),
      { once: true },
    )
    return request.promise
  }
}

interface Deferred<T> {
  readonly promise: Promise<T>
  readonly resolve: (value: T) => void
  readonly reject: (error: Error) => void
}

function deferred<T>(): Deferred<T> {
  let resolvePromise: ((value: T) => void) | undefined
  let rejectPromise: ((error: Error) => void) | undefined
  const promise = new Promise<T>((resolve, reject) => {
    resolvePromise = resolve
    rejectPromise = reject
  })
  return {
    promise,
    resolve: (value) => {
      resolvePromise?.(value)
    },
    reject: (error) => {
      rejectPromise?.(error)
    },
  }
}

function outputMessage(
  seq: bigint,
  offset: bigint,
  data: ReadonlyArray<number>,
): TerminalOutput {
  const bytes = Uint8Array.from(data)
  return {
    kind: 'output',
    agent_id: 'agent-1',
    seq: decimalString(seq),
    offset: decimalString(offset),
    data: bytes,
    historical: false,
    cursor: {
      seq: decimalString(seq),
      next_offset: decimalString(offset + BigInt(bytes.byteLength)),
    },
  }
}

function resizeMessage(
  seq: bigint,
  offset: bigint,
  rows: number,
  columns: number,
): TerminalResize {
  return {
    kind: 'resized',
    agent_id: 'agent-1',
    seq: decimalString(seq),
    rows,
    columns,
    output_offset: decimalString(offset),
    cursor: {
      seq: decimalString(seq),
      next_offset: decimalString(offset),
    },
    historical: false,
  }
}

function eventMessage(
  seq: bigint,
  offset: bigint,
  atMillis: number,
): TerminalEvent {
  return {
    kind: 'event',
    agent_id: 'agent-1',
    event: {
      seq: decimalString(seq),
      timestamp: timestamp(atMillis),
      type: 'output.chunk',
    },
    cursor: {
      seq: decimalString(seq),
      next_offset: decimalString(offset),
    },
    historical: false,
  }
}

function caughtUpMessage(
  mode: 'raw' | 'events',
  seq: bigint,
  offset: bigint,
): TerminalCaughtUp {
  return {
    kind: 'caught_up',
    agent_id: 'agent-1',
    mode,
    cursor: {
      seq: decimalString(seq),
      next_offset: decimalString(offset),
    },
  }
}

function timeline(missing: ReadonlyArray<OutputRange> = []): TerminalTimeline {
  return {
    sessionID: 'agent-1',
    agentID: 'agent-1',
    captured: { seq: 3n, nextOffset: 2n },
    capturedAt: timestamp(3_000),
    durationMillis: 3_000,
    output: {
      range: { start: 0n, end: 2n },
      retained: [{ start: 0n, end: 2n }],
      missing,
    },
    spans: [],
    blocked: [],
  }
}

function framePreview(): TerminalFramePreview {
  return {
    kind: 'frame_preview',
    sessionID: 'agent-1',
    cursor: { seq: 3n, nextOffset: 2n },
    rows: 40,
    columns: 120,
    lines: ['ab'],
    truncated: false,
    fidelity: 'exact_origin_replay',
    restorable: false,
  }
}

function timestamp(epochMillis: number): Timestamp {
  return {
    iso: new Date(epochMillis).toISOString(),
    epochMillis,
  }
}

function utf8Length(value: string): number {
  return new TextEncoder().encode(value).byteLength
}

async function promiseState(
  promise: Promise<void>,
): Promise<'pending' | 'settled'> {
  const settled: Promise<'settled'> = promise.then(() => 'settled')
  const pending: Promise<'pending'> = Promise.resolve('pending')
  return Promise.race([settled, pending])
}
