import {
  getAgentFrame,
  getAgentTimeline,
  OutputExpiredError,
} from '../api/client'
import type {
  FrameSelector,
  OutputRange,
  TerminalCursor,
  TerminalFramePreview,
  TerminalMessage,
  TerminalTimeline,
  Timestamp,
} from '../api/types'
import {
  TerminalStream,
  decimalString,
} from '../ws/terminalStream'
import type {
  TerminalStart,
  TerminalStreamOptions,
  TerminalSubscription,
} from '../ws/terminalStream'
import {
  recordedOperationFromMessage,
  recordedTimestampFromMessage,
} from './recordingBoundary'
import { SessionTape } from './sessionTape'
import type {
  ReplayPlan,
  TapeRecordResult,
  TimedOperation,
} from './sessionTape'
import { createXtermAdapter } from './xtermAdapter'
import type {
  TerminalAdapter,
  TerminalAdapterFactory,
  TerminalDimensions,
  TerminalDisposable,
} from './xtermAdapter'

const INITIAL_DIMENSIONS: TerminalDimensions = { rows: 40, columns: 120 }
const INPUT_LIMIT_BYTES = 64 * 1024
const REPLAY_BATCH_SIZE = 128
const RECONNECT_DELAYS_MS: ReadonlyArray<number> = [
  250,
  500,
  1_000,
  2_000,
  5_000,
]
export type TerminalAccess = 'read_only' | 'read_write'
export type PlaybackSpeed = 0.5 | 1 | 2 | 4
export type TerminalConnectionState =
  | 'loading'
  | 'connecting'
  | 'syncing'
  | 'live'
  | 'reconnecting'
  | 'error'
  | 'disposed'

export type TerminalReplayView =
  | { readonly kind: 'live' }
  | { readonly kind: 'building'; readonly atMillis: number }
  | {
      readonly kind: 'ready'
      readonly atMillis: number
      readonly playback: 'paused' | 'playing'
      readonly speed: PlaybackSpeed
    }
  | { readonly kind: 'not_ready'; readonly atMillis: number }
  | {
      readonly kind: 'expired'
      readonly atMillis: number
      readonly missing: ReadonlyArray<OutputRange>
    }
  | {
      readonly kind: 'local_limit'
      readonly atMillis: number
      readonly byteLimit: number
    }
  | {
      readonly kind: 'error'
      readonly atMillis: number
      readonly message: string
    }

export interface AgentTerminalView {
  readonly agentID: string
  readonly access: TerminalAccess
  readonly connection: TerminalConnectionState
  readonly connectionError: string | null
  readonly timeline: TerminalTimeline | null
  readonly replay: TerminalReplayView
  readonly previewAtMillis: number | null
  readonly frameLoading: boolean
  readonly framePreview: TerminalFramePreview | null
  readonly frameError: string | null
  readonly inputEnabled: boolean
}

export type PlaybackAction =
  | { readonly kind: 'go_live' }
  | { readonly kind: 'preview_time'; readonly atMillis: number }
  | { readonly kind: 'commit_seek'; readonly atMillis: number }
  | { readonly kind: 'cancel_seek' }
  | { readonly kind: 'jump_blocked'; readonly occurrence: number }
  | { readonly kind: 'play' }
  | { readonly kind: 'pause' }
  | { readonly kind: 'set_speed'; readonly speed: PlaybackSpeed }
  | { readonly kind: 'retry' }

export interface TerminalControllerStore {
  start(): Promise<void>
  getSnapshot(): AgentTerminalView
  subscribe(listener: () => void): () => void
  bindViewport(element: HTMLDivElement | null): void
  dispatch(action: PlaybackAction): void
  dispose(): void
}

interface TerminalTransport {
  connect(): Promise<void>
  close(): void
  subscribe(subscription: TerminalSubscription): Promise<void>
  sendInput(agentID: string, data: string): Promise<number>
  resize(agentID: string, rows: number, columns: number): Promise<void>
}

interface ResizeObserverHandle {
  observe(target: Element): void
  disconnect(): void
}

export interface TerminalControllerDependencies {
  readonly createTerminal: TerminalAdapterFactory
  readonly createTape: () => SessionTape
  readonly createStream: (
    options: TerminalStreamOptions,
  ) => TerminalTransport
  readonly loadTimeline: (
    agentID: string,
    signal?: AbortSignal,
  ) => Promise<TerminalTimeline>
  readonly loadFrame: (
    agentID: string,
    selector: FrameSelector,
    signal?: AbortSignal,
  ) => Promise<TerminalFramePreview>
  readonly sleep: (milliseconds: number, signal: AbortSignal) => Promise<void>
  readonly yieldToBrowser: (signal: AbortSignal) => Promise<void>
  readonly createResizeObserver: (
    listener: () => void,
  ) => ResizeObserverHandle | null
}

export interface TerminalSessionControllerOptions {
  readonly agentID: string
  readonly access: TerminalAccess
  readonly dependencies?: Partial<TerminalControllerDependencies>
}

interface ActiveStream {
  readonly generation: number
  readonly transport: TerminalTransport
}

export class TerminalSessionController implements TerminalControllerStore {
  private readonly agentID: string
  private readonly access: TerminalAccess
  private readonly dependencies: TerminalControllerDependencies
  private readonly tape: SessionTape
  private readonly listeners = new Set<() => void>()
  private readonly liveTerminal: TerminalAdapter
  private readonly inputDisposable: TerminalDisposable

  private snapshot: AgentTerminalView
  private viewport: HTMLDivElement | null = null
  private resizeObserver: ResizeObserverHandle | null = null
  private replayTerminal: TerminalAdapter | null = null
  private timelineAbort: AbortController | null = null
  private seekAbort: AbortController | null = null
  private playbackAbort: AbortController | null = null
  private reconnectAbort: AbortController | null = null
  private reconnectTask: Promise<void> | null = null
  private activeStream: ActiveStream | null = null
  private applicationBarrier: Promise<void> = Promise.resolve()
  private startPromise: Promise<void> | null = null
  private inputTail: Promise<void> = Promise.resolve()

  private runGeneration = 0
  private streamGeneration = 0
  private seekGeneration = 0
  private inputGeneration = 0
  private reconnectAttempt = 0
  private started = false
  private disposed = false
  private rawCaughtUp = false
  private rawCursor = originTransportCursor()
  private eventCursor = originTransportCursor()
  private replayOperationIndex = 0
  private replayAtMillis = 0
  private speed: PlaybackSpeed = 1
  private durableDimensions: TerminalDimensions = INITIAL_DIMENSIONS
  private lastProposedDimensions: TerminalDimensions | null = null
  private queuedDimensions: TerminalDimensions | null = null
  private resizeInFlight = false

  constructor(options: TerminalSessionControllerOptions) {
    this.agentID = options.agentID
    this.access = options.access
    this.dependencies = resolveDependencies(options.dependencies)
    this.tape = this.dependencies.createTape()
    this.liveTerminal = this.dependencies.createTerminal({
      kind: 'live',
      ...INITIAL_DIMENSIONS,
    })
    this.inputDisposable = this.liveTerminal.onInput((data) => {
      this.enqueueInput(data)
    })
    this.snapshot = createInitialAgentTerminalView(options.agentID, options.access)
  }

  start(): Promise<void> {
    if (this.disposed) return Promise.resolve()
    if (this.started) return this.startPromise ?? Promise.resolve()
    if (this.startPromise !== null) return this.startPromise
    this.started = true
    const run = ++this.runGeneration
    this.startPromise = this.initialize(run).finally(() => {
      if (run === this.runGeneration) this.startPromise = null
    })
    return this.startPromise
  }

  getSnapshot = (): AgentTerminalView => this.snapshot

  subscribe = (listener: () => void): (() => void) => {
    if (this.disposed) return () => {}
    this.listeners.add(listener)
    return () => {
      this.listeners.delete(listener)
    }
  }

  bindViewport = (element: HTMLDivElement | null): void => {
    if (this.disposed || this.viewport === element) return
    this.resizeObserver?.disconnect()
    this.resizeObserver = null
    this.viewport = element
    this.mountActiveTerminal()
    if (element !== null) {
      this.resizeObserver = this.dependencies.createResizeObserver(() => {
        this.proposeResize()
      })
      this.resizeObserver?.observe(element)
      this.proposeResize()
    }
  }

  dispatch = (action: PlaybackAction): void => {
    if (this.disposed) return
    switch (action.kind) {
      case 'go_live':
        this.goLive()
        return
      case 'preview_time':
        if (validMillis(action.atMillis)) {
          this.update({ previewAtMillis: action.atMillis })
        }
        return
      case 'commit_seek':
        this.commitSeek(action.atMillis)
        return
      case 'cancel_seek':
        this.cancelSeek()
        return
      case 'jump_blocked':
        this.jumpBlocked(action.occurrence)
        return
      case 'play':
        this.play()
        return
      case 'pause':
        this.pause()
        return
      case 'set_speed':
        this.setSpeed(action.speed)
        return
      case 'retry':
        this.retry()
        return
      default: {
        const exhaustive: never = action
        throw new Error(`Unsupported playback action ${String(exhaustive)}`)
      }
    }
  }

  dispose = (): void => {
    if (this.disposed) return
    this.disposed = true
    this.runGeneration += 1
    this.streamGeneration += 1
    this.seekGeneration += 1
    this.inputGeneration += 1
    this.timelineAbort?.abort()
    this.seekAbort?.abort()
    this.playbackAbort?.abort()
    this.reconnectAbort?.abort()
    this.timelineAbort = null
    this.seekAbort = null
    this.playbackAbort = null
    this.reconnectAbort = null
    this.activeStream?.transport.close()
    this.activeStream = null
    this.resizeObserver?.disconnect()
    this.resizeObserver = null
    this.inputDisposable.dispose()
    this.disposeReplayTerminal()
    this.liveTerminal.dispose()
    this.snapshot = {
      ...this.snapshot,
      connection: 'disposed',
      inputEnabled: false,
    }
    this.emit()
    this.listeners.clear()
  }

  private async initialize(run: number): Promise<void> {
    this.timelineAbort?.abort()
    const abort = new AbortController()
    this.timelineAbort = abort
    this.update({
      connection: 'loading',
      connectionError: null,
      inputEnabled: false,
    })
    try {
      const timeline = await this.dependencies.loadTimeline(
        this.agentID,
        abort.signal,
      )
      if (!this.isCurrentRun(run) || abort.signal.aborted) return
      if (timeline.agentID !== this.agentID) {
        throw new Error('Terminal timeline does not match the requested agent')
      }
      this.update({ timeline })
      try {
        await this.connectStream(false)
      } catch (error) {
        if (!this.isCurrentRun(run) || isAbortError(error)) return
        this.beginReconnect(error)
      }
    } catch (error) {
      if (!this.isCurrentRun(run) || isAbortError(error)) return
      this.update({
        connection: 'error',
        connectionError: errorMessage(error),
        inputEnabled: false,
      })
    } finally {
      if (this.timelineAbort === abort) this.timelineAbort = null
    }
  }

  private async connectStream(reconnecting: boolean): Promise<void> {
    if (this.disposed) return
    await this.applicationBarrier
    if (this.disposed) return

    const generation = ++this.streamGeneration
    this.rawCaughtUp = false
    this.inputGeneration += 1
    this.update({
      connection: reconnecting ? 'reconnecting' : 'connecting',
      connectionError: null,
      inputEnabled: false,
    })
    const transport = this.dependencies.createStream({
      onMessage: (message) => {
        const applying = this.applyMessage(generation, message)
        this.applicationBarrier = applying.catch(() => {})
        return applying
      },
      onError: (error) => {
        this.handleStreamFailure(generation, error)
      },
      onStateChange: (state) => {
        if (state === 'closed') {
          this.handleStreamFailure(
            generation,
            new Error('Terminal stream closed'),
          )
        }
      },
    })
    this.activeStream = { generation, transport }

    try {
      await transport.connect()
      if (!this.continueStreamSetup(generation)) return
      await transport.subscribe(this.rawSubscription())
      if (!this.continueStreamSetup(generation)) return
      await transport.subscribe(this.eventSubscription())
      if (!this.continueStreamSetup(generation)) return
      this.reconnectAttempt = 0
      if (!this.rawCaughtUp) this.update({ connection: 'syncing' })
    } catch (error) {
      if (this.disposed || generation !== this.streamGeneration) return
      if (this.activeStream?.generation === generation) {
        this.activeStream = null
        transport.close()
      }
      throw error
    }
  }

  private rawSubscription(): TerminalSubscription {
    const start = startAt(this.rawCursor)
    const viewport = this.liveTerminal.proposeDimensions()
    if (this.access === 'read_write') {
      return {
        agent_id: this.agentID,
        mode: 'raw',
        writable: true,
        ...(viewport === undefined ? {} : { viewport }),
        ...(start === undefined ? {} : { start }),
      }
    }
    return {
      agent_id: this.agentID,
      mode: 'raw',
      writable: false,
      ...(start === undefined ? {} : { start }),
    }
  }

  private eventSubscription(): TerminalSubscription {
    const start = startAt(this.eventCursor)
    return {
      agent_id: this.agentID,
      mode: 'events',
      ...(start === undefined ? {} : { start }),
    }
  }

  private async applyMessage(
    generation: number,
    message: TerminalMessage,
  ): Promise<void> {
    if (!this.isApplicableGeneration(generation)) return
    if (message.agent_id !== this.agentID) {
      throw new Error('Terminal stream returned another agent')
    }

    switch (message.kind) {
      case 'output':
      case 'resized': {
        const result = this.tape.recordOperation(
          recordedOperationFromMessage(message),
        )
        requireTapeAcceptance(result)
        if (result.kind !== 'duplicate') {
          if (message.kind === 'output') {
            await this.liveTerminal.write(message.data)
          } else {
            const dimensions = {
              rows: message.rows,
              columns: message.columns,
            }
            await this.liveTerminal.resize(dimensions)
            this.durableDimensions = dimensions
            this.lastProposedDimensions = null
          }
        }
        if (!this.isApplicableGeneration(generation)) return
        this.rawCursor = laterTransportCursor(this.rawCursor, message.cursor)
        if (message.kind === 'resized') this.proposeResize()
        return
      }
      case 'event': {
        requireTapeAcceptance(
          this.tape.recordTimestamp(recordedTimestampFromMessage(message)),
        )
        if (!this.isApplicableGeneration(generation)) return
        this.eventCursor = laterTransportCursor(this.eventCursor, message.cursor)
        return
      }
      case 'caught_up':
        if (message.mode === 'raw') {
          this.rawCursor = advanceRawCaughtUpCursor(
            this.rawCursor,
            message.cursor,
          )
          this.rawCaughtUp = true
          this.update({
            connection: 'live',
            connectionError: null,
            inputEnabled:
              this.access === 'read_write' &&
              this.snapshot.replay.kind === 'live',
          })
          if (
            this.access === 'read_write' &&
            this.snapshot.replay.kind === 'live'
          ) {
            this.liveTerminal.focus()
          }
          this.proposeResize()
        } else {
          requireCursorMatch(this.eventCursor, message.cursor, 'events')
        }
        return
      case 'snapshot':
        throw new Error('Terminal controller received an unexpected snapshot')
      default: {
        const exhaustive: never = message
        throw new Error(`Unsupported terminal message ${String(exhaustive)}`)
      }
    }
  }

  private handleStreamFailure(generation: number, error: Error): void {
    if (this.disposed || generation !== this.streamGeneration) return
    const active = this.activeStream
    if (active === null || active.generation !== generation) return
    this.activeStream = null
    active.transport.close()
    this.rawCaughtUp = false
    this.inputGeneration += 1
    this.update({
      connection: 'reconnecting',
      connectionError: error.message,
      inputEnabled: false,
    })
    this.beginReconnect(error)
  }

  private beginReconnect(error: unknown): void {
    if (this.disposed || this.reconnectTask !== null) return
    const abort = new AbortController()
    this.reconnectAbort?.abort()
    this.reconnectAbort = abort
    this.update({
      connection: 'reconnecting',
      connectionError: errorMessage(error),
      inputEnabled: false,
    })
    const task = this.reconnectLoop(abort.signal)
    this.reconnectTask = task
    void task.finally(() => {
      if (this.reconnectTask === task) this.reconnectTask = null
      if (this.reconnectAbort === abort) this.reconnectAbort = null
    })
  }

  private async reconnectLoop(signal: AbortSignal): Promise<void> {
    while (!this.disposed && !signal.aborted) {
      const delay =
        RECONNECT_DELAYS_MS[
          Math.min(this.reconnectAttempt, RECONNECT_DELAYS_MS.length - 1)
        ]
      this.reconnectAttempt += 1
      try {
        await this.dependencies.sleep(delay, signal)
        await this.connectStream(true)
        return
      } catch (error) {
        if (this.disposed || signal.aborted || isAbortError(error)) return
        this.update({
          connection: 'reconnecting',
          connectionError: errorMessage(error),
          inputEnabled: false,
        })
      }
    }
  }

  private enqueueInput(data: string): void {
    if (
      this.disposed ||
      !this.rawCaughtUp ||
      this.access !== 'read_write' ||
      this.snapshot.replay.kind !== 'live' ||
      data.length === 0
    ) {
      return
    }
    const chunks = splitUTF8(data)
    const generation = this.inputGeneration
    this.inputTail = this.inputTail
      .then(async () => {
        for (const chunk of chunks) {
          if (
            this.disposed ||
            generation !== this.inputGeneration ||
            !this.rawCaughtUp
          ) {
            return
          }
          const active = this.activeStream
          if (active === null) return
          const bytes = utf8Length(chunk)
          const acknowledged = await active.transport.sendInput(
            this.agentID,
            chunk,
          )
          if (acknowledged !== bytes) {
            throw new Error('Terminal input acknowledgement byte count differs')
          }
        }
      })
      .catch((error) => {
        const active = this.activeStream
        if (active !== null) {
          this.handleStreamFailure(active.generation, toError(error))
        }
      })
  }

  private proposeResize(): void {
    if (
      this.disposed ||
      this.access !== 'read_write' ||
      !this.rawCaughtUp ||
      this.snapshot.replay.kind !== 'live'
    ) {
      return
    }
    const dimensions = this.liveTerminal.proposeDimensions()
    if (
      dimensions === undefined ||
      dimensionsEqual(dimensions, this.durableDimensions) ||
      (this.lastProposedDimensions !== null &&
        dimensionsEqual(dimensions, this.lastProposedDimensions))
    ) {
      return
    }
    this.queuedDimensions = dimensions
    this.pumpResize()
  }

  private pumpResize(): void {
    if (this.disposed || this.resizeInFlight) return
    const dimensions = this.queuedDimensions
    const active = this.activeStream
    if (dimensions === null || active === null || !this.rawCaughtUp) return
    this.queuedDimensions = null
    this.resizeInFlight = true
    this.lastProposedDimensions = dimensions
    void active.transport
      .resize(this.agentID, dimensions.rows, dimensions.columns)
      .catch((error) => {
        this.handleStreamFailure(active.generation, toError(error))
      })
      .finally(() => {
        this.resizeInFlight = false
        if (this.queuedDimensions !== null) this.pumpResize()
      })
  }

  private commitSeek(atMillis: number): void {
    if (!validMillis(atMillis)) {
      this.update({
        replay: {
          kind: 'error',
          atMillis,
          message: 'Replay time is invalid',
        },
      })
      return
    }
    this.pausePlayback()
    this.seekAbort?.abort()
    this.disposeReplayTerminal()
    const abort = new AbortController()
    this.seekAbort = abort
    const generation = ++this.seekGeneration
    this.update({
      previewAtMillis: atMillis,
      frameLoading: true,
      framePreview: null,
      frameError: null,
      inputEnabled: false,
      replay: { kind: 'building', atMillis },
    })
    this.mountActiveTerminal()
    void this.loadFramePreview(generation, atMillis, abort.signal)
    void this.rebuildReplay(generation, atMillis, abort.signal)
  }

  private async loadFramePreview(
    generation: number,
    atMillis: number,
    signal: AbortSignal,
  ): Promise<void> {
    try {
      const selector: FrameSelector = {
        kind: 'timestamp',
        at: timestampFromMillis(atMillis),
      }
      const frame = await this.dependencies.loadFrame(
        this.agentID,
        selector,
        signal,
      )
      if (!this.isCurrentSeek(generation, signal)) return
      this.update({
        frameLoading: false,
        framePreview: frame,
        frameError: null,
      })
    } catch (error) {
      if (!this.isCurrentSeek(generation, signal) || isAbortError(error)) return
      const message =
        error instanceof OutputExpiredError
          ? 'Recorded terminal output has expired'
          : errorMessage(error)
      this.update({
        frameLoading: false,
        framePreview: null,
        frameError: message,
      })
    }
  }

  private async rebuildReplay(
    generation: number,
    atMillis: number,
    signal: AbortSignal,
  ): Promise<void> {
    const plan = this.tape.planAt({
      atMillis,
      missing: this.snapshot.timeline?.output.missing,
    })
    if (!this.isCurrentSeek(generation, signal)) return
    if (plan.kind !== 'exact') {
      this.update({ replay: replayViewFromPlan(plan, atMillis) })
      return
    }

    this.disposeReplayTerminal()
    const terminal = this.dependencies.createTerminal({
      kind: 'replay',
      ...INITIAL_DIMENSIONS,
    })
    this.replayTerminal = terminal
    this.replayOperationIndex = 0
    this.replayAtMillis = atMillis
    this.mountActiveTerminal()

    try {
      await this.applyReplayOperations(
        terminal,
        plan.operations,
        generation,
        signal,
      )
      if (!this.isCurrentSeek(generation, signal)) return
      this.replayOperationIndex = plan.operations.length
      this.replayAtMillis = atMillis
      this.update({
        replay: {
          kind: 'ready',
          atMillis,
          playback: 'paused',
          speed: this.speed,
        },
      })
    } catch (error) {
      if (!this.isCurrentSeek(generation, signal) || isAbortError(error)) return
      this.update({
        replay: {
          kind: 'error',
          atMillis,
          message: errorMessage(error),
        },
      })
    }
  }

  private async applyReplayOperations(
    terminal: TerminalAdapter,
    operations: ReadonlyArray<TimedOperation>,
    generation: number,
    signal: AbortSignal,
  ): Promise<void> {
    let batch = 0
    for (const timed of operations) {
      if (!this.isCurrentSeek(generation, signal)) return
      await applyRecordedOperation(terminal, timed)
      this.replayOperationIndex += 1
      batch += 1
      if (batch === REPLAY_BATCH_SIZE) {
        batch = 0
        await this.dependencies.yieldToBrowser(signal)
      }
    }
  }

  private play(): void {
    if (
      this.snapshot.replay.kind !== 'ready' ||
      this.snapshot.replay.playback === 'playing' ||
      this.replayTerminal === null
    ) {
      return
    }
    this.pausePlayback()
    const abort = new AbortController()
    this.playbackAbort = abort
    this.update({
      replay: {
        kind: 'ready',
        atMillis: this.replayAtMillis,
        playback: 'playing',
        speed: this.speed,
      },
    })
    void this.playbackLoop(abort.signal)
  }

  private async playbackLoop(signal: AbortSignal): Promise<void> {
    let batch = 0
    let operations: ReadonlyArray<TimedOperation> = []
    try {
      while (!this.disposed && !signal.aborted) {
        if (this.replayOperationIndex >= operations.length) {
          const plan = this.tape.plan({
            target: this.tape.exactFrontier(),
            missing: this.snapshot.timeline?.output.missing,
          })
          if (plan.kind !== 'exact') {
            this.update({
              replay: replayViewFromPlan(plan, this.replayAtMillis),
            })
            return
          }
          if (plan.operations.length <= this.replayOperationIndex) break
          operations = plan.operations
        }
        const next = operations[this.replayOperationIndex]
        if (next === undefined) break
        const delay = Math.max(
          0,
          (next.atMillis - this.replayAtMillis) / this.speed,
        )
        await this.dependencies.sleep(delay, signal)
        if (signal.aborted || this.replayTerminal === null) return
        await applyRecordedOperation(this.replayTerminal, next)
        if (signal.aborted) return
        this.replayOperationIndex += 1
        this.replayAtMillis = next.atMillis
        this.update({
          replay: {
            kind: 'ready',
            atMillis: next.atMillis,
            playback: 'playing',
            speed: this.speed,
          },
        })
        batch += 1
        if (batch === REPLAY_BATCH_SIZE) {
          batch = 0
          await this.dependencies.yieldToBrowser(signal)
        }
      }
    } catch (error) {
      if (!signal.aborted && !isAbortError(error)) {
        this.update({
          replay: {
            kind: 'error',
            atMillis: this.replayAtMillis,
            message: errorMessage(error),
          },
        })
        return
      }
    }
    if (!signal.aborted && this.snapshot.replay.kind === 'ready') {
      this.update({
        replay: {
          kind: 'ready',
          atMillis: this.replayAtMillis,
          playback: 'paused',
          speed: this.speed,
        },
      })
    }
  }

  private pause(): void {
    if (
      this.snapshot.replay.kind !== 'ready' ||
      this.snapshot.replay.playback !== 'playing'
    ) {
      return
    }
    this.pausePlayback()
    this.update({
      replay: {
        kind: 'ready',
        atMillis: this.replayAtMillis,
        playback: 'paused',
        speed: this.speed,
      },
    })
  }

  private setSpeed(speed: PlaybackSpeed): void {
    const wasPlaying =
      this.snapshot.replay.kind === 'ready' &&
      this.snapshot.replay.playback === 'playing'
    this.speed = speed
    if (this.snapshot.replay.kind === 'ready') {
      this.pausePlayback()
      this.update({
        replay: {
          kind: 'ready',
          atMillis: this.replayAtMillis,
          playback: 'paused',
          speed,
        },
      })
      if (wasPlaying) this.play()
    }
  }

  private jumpBlocked(occurrence: number): void {
    const blocked = this.snapshot.timeline?.blocked.find(
      (item) => item.number === occurrence,
    )
    if (blocked !== undefined) {
      this.commitSeek(blocked.span.startAt.epochMillis)
    }
  }

  private cancelSeek(): void {
    this.seekGeneration += 1
    this.seekAbort?.abort()
    this.seekAbort = null
    this.goLive()
  }

  private goLive(): void {
    this.pausePlayback()
    this.seekGeneration += 1
    this.seekAbort?.abort()
    this.seekAbort = null
    this.disposeReplayTerminal()
    this.update({
      replay: { kind: 'live' },
      previewAtMillis: null,
      frameLoading: false,
      framePreview: null,
      frameError: null,
      inputEnabled: this.rawCaughtUp && this.access === 'read_write',
    })
    this.mountActiveTerminal()
    this.liveTerminal.focus()
    this.proposeResize()
  }

  private retry(): void {
    if (this.disposed) return
    this.started = false
    this.runGeneration += 1
    this.streamGeneration += 1
    this.inputGeneration += 1
    this.timelineAbort?.abort()
    this.reconnectAbort?.abort()
    this.activeStream?.transport.close()
    this.activeStream = null
    this.reconnectTask = null
    this.reconnectAttempt = 0
    this.startPromise = null
    void this.start()
  }

  private pausePlayback(): void {
    this.playbackAbort?.abort()
    this.playbackAbort = null
  }

  private disposeReplayTerminal(): void {
    const terminal = this.replayTerminal
    this.replayTerminal = null
    terminal?.dispose()
  }

  private mountActiveTerminal(): void {
    this.liveTerminal.mount(null)
    this.replayTerminal?.mount(null)
    if (this.viewport === null) return
    if (this.snapshot.replay.kind === 'live' || this.replayTerminal === null) {
      this.liveTerminal.mount(this.viewport)
    } else {
      this.replayTerminal.mount(this.viewport)
    }
  }

  private update(changes: Partial<AgentTerminalView>): void {
    if (this.disposed && changes.connection !== 'disposed') return
    this.snapshot = { ...this.snapshot, ...changes }
    this.emit()
  }

  private emit(): void {
    for (const listener of this.listeners) listener()
  }

  private isCurrentRun(run: number): boolean {
    return !this.disposed && run === this.runGeneration
  }

  private continueStreamSetup(generation: number): boolean {
    if (this.disposed || generation !== this.streamGeneration) return false
    if (this.activeStream?.generation !== generation) {
      throw new Error('Terminal stream closed during subscription setup')
    }
    return true
  }

  private isApplicableGeneration(generation: number): boolean {
    return !this.disposed && generation === this.streamGeneration
  }

  private isCurrentSeek(
    generation: number,
    signal: AbortSignal,
  ): boolean {
    return (
      !this.disposed &&
      !signal.aborted &&
      generation === this.seekGeneration
    )
  }
}

export function createInitialAgentTerminalView(
  agentID: string,
  access: TerminalAccess,
): AgentTerminalView {
  return {
    agentID,
    access,
    connection: 'loading',
    connectionError: null,
    timeline: null,
    replay: { kind: 'live' },
    previewAtMillis: null,
    frameLoading: false,
    framePreview: null,
    frameError: null,
    inputEnabled: false,
  }
}

function resolveDependencies(
  overrides: Partial<TerminalControllerDependencies> | undefined,
): TerminalControllerDependencies {
  return {
    createTerminal: overrides?.createTerminal ?? createXtermAdapter,
    createTape: overrides?.createTape ?? (() => new SessionTape()),
    createStream:
      overrides?.createStream ??
      ((options) => new TerminalStream(options)),
    loadTimeline: overrides?.loadTimeline ?? getAgentTimeline,
    loadFrame: overrides?.loadFrame ?? getAgentFrame,
    sleep: overrides?.sleep ?? abortableDelay,
    yieldToBrowser:
      overrides?.yieldToBrowser ??
      ((signal) => abortableDelay(0, signal)),
    createResizeObserver:
      overrides?.createResizeObserver ??
      ((listener) => {
        if (typeof ResizeObserver === 'undefined') return null
        return new ResizeObserver(listener)
      }),
  }
}

function originTransportCursor(): TerminalCursor {
  return { seq: decimalString(0n), next_offset: decimalString(0n) }
}

function startAt(
  cursor: TerminalCursor,
): TerminalStart | undefined {
  if (cursor.seq === '0' && cursor.next_offset === '0') return undefined
  return { kind: 'cursor', cursor: cloneTransportCursor(cursor) }
}

function cloneTransportCursor(cursor: TerminalCursor): TerminalCursor {
  return { seq: cursor.seq, next_offset: cursor.next_offset }
}

function laterTransportCursor(
  current: TerminalCursor,
  candidate: TerminalCursor,
): TerminalCursor {
  const currentSeq = BigInt(current.seq)
  const candidateSeq = BigInt(candidate.seq)
  if (
    candidateSeq > currentSeq ||
    (candidateSeq === currentSeq &&
      BigInt(candidate.next_offset) > BigInt(current.next_offset))
  ) {
    return cloneTransportCursor(candidate)
  }
  return current
}

function requireCursorMatch(
  applied: TerminalCursor,
  caughtUp: TerminalCursor,
  mode: 'events',
): void {
  if (
    applied.seq !== caughtUp.seq ||
    applied.next_offset !== caughtUp.next_offset
  ) {
    throw new Error(
      `Terminal ${mode} caught_up cursor does not match locally applied messages`,
    )
  }
}

function advanceRawCaughtUpCursor(
  applied: TerminalCursor,
  caughtUp: TerminalCursor,
): TerminalCursor {
  if (
    BigInt(caughtUp.seq) < BigInt(applied.seq) ||
    caughtUp.next_offset !== applied.next_offset
  ) {
    throw new Error(
      'Terminal raw caught_up cursor does not match locally applied output',
    )
  }
  return cloneTransportCursor(caughtUp)
}

function requireTapeAcceptance(result: TapeRecordResult): void {
  if (result.kind === 'rejected') {
    throw new Error(`Terminal recording rejected ${result.reason}`)
  }
}

function replayViewFromPlan(
  plan: Exclude<ReplayPlan, { readonly kind: 'exact' }>,
  atMillis: number,
): TerminalReplayView {
  switch (plan.kind) {
    case 'not_ready':
      return { kind: 'not_ready', atMillis }
    case 'expired':
      return { kind: 'expired', atMillis, missing: plan.missing }
    case 'local_limit':
      return {
        kind: 'local_limit',
        atMillis,
        byteLimit: plan.byteLimit,
      }
    default: {
      const exhaustive: never = plan
      throw new Error(`Unsupported replay plan ${String(exhaustive)}`)
    }
  }
}

async function applyRecordedOperation(
  terminal: TerminalAdapter,
  timed: TimedOperation,
): Promise<void> {
  switch (timed.operation.kind) {
    case 'output':
      await terminal.write(timed.operation.data)
      return
    case 'resize':
      await terminal.resize({
        rows: timed.operation.rows,
        columns: timed.operation.columns,
      })
      return
    default: {
      const exhaustive: never = timed.operation
      throw new Error(`Unsupported replay operation ${String(exhaustive)}`)
    }
  }
}

function splitUTF8(data: string): string[] {
  const normalized = new TextDecoder().decode(new TextEncoder().encode(data))
  const chunks: string[] = []
  let chunk = ''
  let bytes = 0
  for (const character of normalized) {
    const characterBytes = utf8Length(character)
    if (bytes + characterBytes > INPUT_LIMIT_BYTES) {
      chunks.push(chunk)
      chunk = ''
      bytes = 0
    }
    chunk += character
    bytes += characterBytes
  }
  if (chunk.length > 0) chunks.push(chunk)
  return chunks
}

function utf8Length(value: string): number {
  return new TextEncoder().encode(value).byteLength
}

function timestampFromMillis(atMillis: number): Timestamp {
  const iso = new Date(atMillis).toISOString()
  return { iso, epochMillis: atMillis }
}

function validMillis(value: number): boolean {
  return Number.isSafeInteger(value) && !Number.isNaN(new Date(value).getTime())
}

function dimensionsEqual(
  left: TerminalDimensions,
  right: TerminalDimensions,
): boolean {
  return left.rows === right.rows && left.columns === right.columns
}

function abortableDelay(
  milliseconds: number,
  signal: AbortSignal,
): Promise<void> {
  if (signal.aborted) {
    return Promise.reject(new DOMException('Aborted', 'AbortError'))
  }
  return new Promise<void>((resolve, reject) => {
    const timer = setTimeout(() => {
      signal.removeEventListener('abort', abort)
      resolve()
    }, milliseconds)
    const abort = () => {
      clearTimeout(timer)
      signal.removeEventListener('abort', abort)
      reject(new DOMException('Aborted', 'AbortError'))
    }
    signal.addEventListener('abort', abort, { once: true })
  })
}

function isAbortError(error: unknown): boolean {
  return error instanceof DOMException && error.name === 'AbortError'
}

function errorMessage(error: unknown): string {
  return toError(error).message
}

function toError(error: unknown): Error {
  return error instanceof Error ? error : new Error(String(error))
}
