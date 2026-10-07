import type {
  ConnectionState,
  TerminalMessage,
  TerminalMode,
  TerminalSnapshot,
} from '../api/types'
import {
  TerminalStream,
  type TerminalStreamOptions,
  type TerminalSubscription,
} from '../ws/terminalStream'

const DEFAULT_RECONNECT_DELAY_MILLIS = 1_000

export interface FleetSnapshotView {
  readonly connection: ConnectionState
  readonly byAgentID: ReadonlyMap<string, TerminalSnapshot>
}

export interface FleetSnapshotStream {
  connect(): Promise<void>
  subscribe(subscription: TerminalSubscription): Promise<void>
  unsubscribe(agentID: string, mode: TerminalMode): Promise<void>
  close(): void
}

export type FleetSnapshotStreamFactory = (
  options: TerminalStreamOptions,
) => FleetSnapshotStream

interface FleetSnapshotControllerOptions {
  readonly createStream?: FleetSnapshotStreamFactory
  readonly reconnectDelayMillis?: number
  readonly onError?: (error: Error) => void
}

interface ActiveStream {
  readonly stream: FleetSnapshotStream
  readonly subscribed: Set<string>
  ready: boolean
}

const defaultStreamFactory: FleetSnapshotStreamFactory = (options) =>
  new TerminalStream(options)

export class FleetSnapshotController {
  private readonly listeners = new Set<() => void>()
  private readonly createStream: FleetSnapshotStreamFactory
  private readonly reconnectDelayMillis: number
  private readonly onError?: (error: Error) => void
  private desiredAgentIDs = new Set<string>()
  private active: ActiveStream | null = null
  private retryTimer: ReturnType<typeof setTimeout> | null = null
  private reconcileQueue = Promise.resolve()
  private started = false
  private view: FleetSnapshotView = {
    connection: 'closed',
    byAgentID: new Map(),
  }

  constructor(options: FleetSnapshotControllerOptions = {}) {
    const reconnectDelayMillis =
      options.reconnectDelayMillis ?? DEFAULT_RECONNECT_DELAY_MILLIS
    if (
      !Number.isFinite(reconnectDelayMillis) ||
      reconnectDelayMillis < 0
    ) {
      throw new Error('Fleet snapshot reconnect delay must be non-negative')
    }
    this.createStream = options.createStream ?? defaultStreamFactory
    this.reconnectDelayMillis = reconnectDelayMillis
    this.onError = options.onError
  }

  getSnapshot = (): FleetSnapshotView => this.view

  subscribe = (listener: () => void): (() => void) => {
    this.listeners.add(listener)
    return () => {
      this.listeners.delete(listener)
    }
  }

  setAgentIDs(agentIDs: ReadonlyArray<string>): void {
    const desired = new Set([...agentIDs].sort())
    if (sameSet(desired, this.desiredAgentIDs)) return
    this.desiredAgentIDs = desired
    this.pruneSnapshots()
    if (desired.size === 0) {
      if (this.retryTimer !== null) {
        clearTimeout(this.retryTimer)
        this.retryTimer = null
      }
      const active = this.active
      this.active = null
      active?.stream.close()
      this.setConnection('closed')
      return
    }
    if (this.started && this.active === null) {
      this.openStream()
      return
    }
    this.queueReconcile()
  }

  start(): void {
    if (this.started) return
    this.started = true
    if (this.desiredAgentIDs.size > 0) this.openStream()
  }

  dispose(): void {
    if (!this.started && this.active === null) return
    this.started = false
    if (this.retryTimer !== null) {
      clearTimeout(this.retryTimer)
      this.retryTimer = null
    }
    const active = this.active
    this.active = null
    active?.stream.close()
    this.setConnection('closed')
  }

  private openStream(): void {
    if (
      !this.started ||
      this.active !== null ||
      this.desiredAgentIDs.size === 0
    ) {
      return
    }
    let active: ActiveStream
    const stream = this.createStream({
      onMessage: (message) => this.handleMessage(active, message),
      onError: (error) => {
        if (this.active === active) this.onError?.(error)
      },
      onStateChange: (connection) => {
        if (this.active !== active) return
        if (connection === 'closed') {
          this.active = null
          this.setConnection('closed')
          this.scheduleReconnect()
          return
        }
        this.setConnection(connection)
      },
    })
    active = {
      stream,
      subscribed: new Set(),
      ready: false,
    }
    this.active = active
    void stream.connect().then(
      () => {
        if (this.active !== active) return
        active.ready = true
        this.setConnection('open')
        this.queueReconcile()
      },
      (error: unknown) => {
        if (this.active !== active) return
        this.active = null
        stream.close()
        this.onError?.(toError(error))
        this.setConnection('closed')
        this.scheduleReconnect()
      },
    )
  }

  private queueReconcile(): void {
    const active = this.active
    if (active === null || !active.ready) return
    this.reconcileQueue = this.reconcileQueue
      .then(() => this.reconcile(active))
      .catch((error: unknown) => {
        if (this.active !== active) return
        this.active = null
        active.stream.close()
        this.onError?.(toError(error))
        this.setConnection('closed')
        this.scheduleReconnect()
      })
  }

  private async reconcile(active: ActiveStream): Promise<void> {
    if (this.active !== active || !active.ready) return
    const removals = [...active.subscribed]
      .filter((agentID) => !this.desiredAgentIDs.has(agentID))
      .sort()
    for (const agentID of removals) {
      await active.stream.unsubscribe(agentID, 'snapshot')
      if (this.active !== active) return
      active.subscribed.delete(agentID)
    }

    const additions = [...this.desiredAgentIDs]
      .filter((agentID) => !active.subscribed.has(agentID))
      .sort()
    for (const agentID of additions) {
      await active.stream.subscribe({ agent_id: agentID, mode: 'snapshot' })
      if (this.active !== active) return
      active.subscribed.add(agentID)
    }
  }

  private handleMessage(active: ActiveStream, message: TerminalMessage): void {
    if (
      this.active !== active ||
      message.kind !== 'snapshot' ||
      !this.desiredAgentIDs.has(message.agent_id)
    ) {
      return
    }
    const byAgentID = new Map(this.view.byAgentID)
    byAgentID.set(message.agent_id, message)
    this.setView({ ...this.view, byAgentID })
  }

  private pruneSnapshots(): void {
    const byAgentID = new Map(
      [...this.view.byAgentID].filter(([agentID]) =>
        this.desiredAgentIDs.has(agentID),
      ),
    )
    if (byAgentID.size === this.view.byAgentID.size) return
    this.setView({ ...this.view, byAgentID })
  }

  private scheduleReconnect(): void {
    if (
      !this.started ||
      this.desiredAgentIDs.size === 0 ||
      this.retryTimer !== null
    ) {
      return
    }
    this.retryTimer = setTimeout(() => {
      this.retryTimer = null
      this.openStream()
    }, this.reconnectDelayMillis)
  }

  private setConnection(connection: ConnectionState): void {
    if (this.view.connection === connection) return
    this.setView({ ...this.view, connection })
  }

  private setView(view: FleetSnapshotView): void {
    this.view = view
    for (const listener of this.listeners) listener()
  }
}

function sameSet(left: ReadonlySet<string>, right: ReadonlySet<string>): boolean {
  if (left.size !== right.size) return false
  for (const value of left) {
    if (!right.has(value)) return false
  }
  return true
}

function toError(value: unknown): Error {
  return value instanceof Error ? value : new Error(String(value))
}
