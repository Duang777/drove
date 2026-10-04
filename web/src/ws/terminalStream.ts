import type {
  ConnectionState,
  DecimalString,
  TerminalCaughtUp,
  TerminalCursor,
  TerminalEvent,
  TerminalEventEnvelope,
  TerminalMessage,
  TerminalMode,
  TerminalOutput,
  TerminalResize,
  TerminalSnapshot,
} from '../api/types'

const WS_PATH = '/ws'
const PROTOCOL = 'drove.v2'
const VERSION = 2
const COMMAND_TIMEOUT_MS = 10_000
const RAW_HEADER_BYTES = 24
const RAW_OUTPUT_KIND = 1
const HISTORICAL_FLAG = 1
const MAX_ID_BYTES = 128
const MAX_OUTPUT_BYTES = 32 * 1024
const MAX_UINT64 = 18_446_744_073_709_551_615n

export type TerminalStart =
  | { kind: 'cursor'; cursor: TerminalCursor }
  | { kind: 'sequence'; seq: DecimalString }
  | { kind: 'offset'; offset: DecimalString }

export type TerminalSubscription =
  | {
      agent_id: string
      mode: 'raw'
      writable?: false
      start?: TerminalStart
    }
  | {
      agent_id: string
      mode: 'raw'
      writable: true
      viewport?: { rows: number; columns: number }
      start?: TerminalStart
    }
  | {
      agent_id: string
      mode: 'events'
      start?: TerminalStart
    }
  | {
      agent_id: string
      mode: 'snapshot'
    }

interface Options {
  onMessage: (message: TerminalMessage) => void | Promise<void>
  onError?: (error: Error) => void
  onStateChange?: (state: ConnectionState) => void
}

type CommandResult =
  | {
      kind: 'subscribed'
      agent_id: string
      mode: TerminalMode
      cursor?: TerminalCursor
    }
  | {
      kind: 'unsubscribed'
      agent_id: string
      mode: TerminalMode
    }
  | {
      kind: 'ack'
      bytes?: number
    }

interface PendingCommand {
  expected: CommandResult['kind']
  resolve: (result: CommandResult) => void
  reject: (error: Error) => void
  timer: ReturnType<typeof setTimeout>
}

interface ConnectPromise {
  resolve: () => void
  reject: (error: Error) => void
}

type ParsedText =
  | { kind: 'hello' }
  | { kind: 'command'; request_id: string; result: CommandResult }
  | { kind: 'error'; error: TerminalProtocolError }
  | { kind: 'stream'; message: Exclude<TerminalMessage, TerminalOutput> }

interface ResumeCursor {
  agent_id: string
  mode: TerminalMode
  cursor: TerminalCursor
}

export class TerminalProtocolError extends Error {
  readonly requestID?: string
  readonly agentID?: string
  readonly mode?: TerminalMode
  readonly code: string
  readonly missing: ReadonlyArray<{ start: DecimalString; end: DecimalString }>
  readonly subscriptions: ReadonlyArray<ResumeCursor>

  constructor(options: {
    requestID?: string
    agentID?: string
    mode?: TerminalMode
    code: string
    message: string
    missing?: Array<{ start: DecimalString; end: DecimalString }>
    subscriptions?: ResumeCursor[]
  }) {
    super(`${options.code}: ${options.message}`)
    this.name = 'TerminalProtocolError'
    this.requestID = options.requestID
    this.agentID = options.agentID
    this.mode = options.mode
    this.code = options.code
    this.missing = options.missing ?? []
    this.subscriptions = options.subscriptions ?? []
  }
}

export class TerminalStream {
  private ws: WebSocket | null = null
  private closed = false
  private connectPromise: ConnectPromise | null = null
  private readonly pending = new Map<string, PendingCommand>()
  private readonly cursors = new Map<string, TerminalCursor>()
  private delivery = Promise.resolve()
  private deliveryStopped = false

  private readonly onMessage: (message: TerminalMessage) => void | Promise<void>
  private readonly onError?: (error: Error) => void
  private readonly onStateChange?: (state: ConnectionState) => void

  constructor(options: Options) {
    this.onMessage = options.onMessage
    this.onError = options.onError
    this.onStateChange = options.onStateChange
  }

  connect(): Promise<void> {
    if (this.closed) {
      return Promise.reject(new Error('Terminal stream is closed'))
    }
    if (this.ws !== null) {
      return Promise.reject(new Error('Terminal stream is already connected'))
    }

    const scheme = window.location.protocol === 'https:' ? 'wss' : 'ws'
    const ws = new WebSocket(`${scheme}://${window.location.host}${WS_PATH}`, PROTOCOL)
    ws.binaryType = 'arraybuffer'
    this.ws = ws
    this.setState('connecting')

    const connected = new Promise<void>((resolve, reject) => {
      this.connectPromise = { resolve, reject }
    })

    ws.onmessage = (event) => {
      try {
        this.receive(event.data)
      } catch (error) {
        const failure = toError(error)
        this.fail(failure)
        this.reportError(failure)
        ws.close()
      }
    }
    ws.onerror = () => {
      ws.close()
    }
    ws.onclose = () => {
      if (this.ws === ws) this.ws = null
      this.closed = true
      this.fail(new Error('Terminal WebSocket closed'))
      this.setState('closed')
    }
    return connected
  }

  close(): void {
    this.closed = true
    const ws = this.ws
    this.ws = null
    ws?.close()
    this.fail(new Error('Terminal stream closed'))
    this.setState('closed')
  }

  async subscribe(subscription: TerminalSubscription): Promise<void> {
    validateSubscription(subscription)
    const requestID = crypto.randomUUID()
    const command: Record<string, unknown> = {
      version: VERSION,
      type: 'subscribe',
      request_id: requestID,
      agent_id: subscription.agent_id,
      mode: subscription.mode,
    }
    if (subscription.mode === 'raw' && subscription.writable === true) {
      command.writable = true
      if (subscription.viewport !== undefined) {
        command.rows = subscription.viewport.rows
        command.columns = subscription.viewport.columns
      }
    }
    if ('start' in subscription && subscription.start !== undefined) {
      switch (subscription.start.kind) {
        case 'cursor':
          command.cursor = subscription.start.cursor
          break
        case 'sequence':
          command.seq = subscription.start.seq
          break
        case 'offset':
          command.offset = subscription.start.offset
          break
        default: {
          const exhaustive: never = subscription.start
          throw new Error(`Unsupported terminal start ${String(exhaustive)}`)
        }
      }
    }
    const result = await this.command(requestID, 'subscribed', command)
    if (
      result.kind !== 'subscribed' ||
      result.agent_id !== subscription.agent_id ||
      result.mode !== subscription.mode
    ) {
      throw new Error('Terminal subscribe response does not match the request')
    }
    if (result.cursor !== undefined) {
      this.cursors.set(subscriptionKey(result.agent_id, result.mode), result.cursor)
    }
  }

  async unsubscribe(agentID: string, mode: TerminalMode): Promise<void> {
    validateAgentID(agentID)
    validateMode(mode)
    const requestID = crypto.randomUUID()
    const result = await this.command(requestID, 'unsubscribed', {
      version: VERSION,
      type: 'unsubscribe',
      request_id: requestID,
      agent_id: agentID,
      mode,
    })
    if (
      result.kind !== 'unsubscribed' ||
      result.agent_id !== agentID ||
      result.mode !== mode
    ) {
      throw new Error('Terminal unsubscribe response does not match the request')
    }
    this.cursors.delete(subscriptionKey(agentID, mode))
  }

  async sendInput(agentID: string, data: string): Promise<number> {
    validateAgentID(agentID)
    const requestID = crypto.randomUUID()
    const result = await this.command(requestID, 'ack', {
      version: VERSION,
      type: 'input',
      request_id: requestID,
      agent_id: agentID,
      data,
    })
    if (result.kind !== 'ack' || result.bytes === undefined) {
      throw new Error('Terminal input acknowledgement omitted bytes')
    }
    return result.bytes
  }

  async resize(agentID: string, rows: number, columns: number): Promise<void> {
    validateAgentID(agentID)
    validateSize(rows, columns)
    const requestID = crypto.randomUUID()
    const result = await this.command(requestID, 'ack', {
      version: VERSION,
      type: 'resize',
      request_id: requestID,
      agent_id: agentID,
      rows,
      columns,
    })
    if (result.kind !== 'ack' || result.bytes !== undefined) {
      throw new Error('Invalid terminal resize acknowledgement')
    }
  }

  cursor(agentID: string, mode: TerminalMode): TerminalCursor | undefined {
    return this.cursors.get(subscriptionKey(agentID, mode))
  }

  private command(
    requestID: string,
    expected: CommandResult['kind'],
    command: Record<string, unknown>,
  ): Promise<CommandResult> {
    const ws = this.ws
    if (ws === null || ws.readyState !== WebSocket.OPEN || this.connectPromise !== null) {
      return Promise.reject(new Error('Terminal WebSocket is not ready'))
    }
    return new Promise<CommandResult>((resolve, reject) => {
      const timer = setTimeout(() => {
        this.pending.delete(requestID)
        reject(new Error(`Terminal command ${requestID} timed out`))
      }, COMMAND_TIMEOUT_MS)
      this.pending.set(requestID, { expected, resolve, reject, timer })
      try {
        ws.send(JSON.stringify(command))
      } catch (error) {
        clearTimeout(timer)
        this.pending.delete(requestID)
        reject(toError(error))
      }
    })
  }

  private receive(data: unknown): void {
    if (typeof data === 'string') {
      this.receiveText(parseTextMessage(data))
      return
    }
    if (data instanceof ArrayBuffer) {
      this.deliver(parseRawFrame(data))
      return
    }
    throw new Error('Terminal WebSocket returned an unsupported frame body')
  }

  private receiveText(parsed: ParsedText): void {
    switch (parsed.kind) {
      case 'hello': {
        const connecting = this.connectPromise
        if (connecting === null) {
          throw new Error('Terminal WebSocket sent an unexpected hello')
        }
        this.connectPromise = null
        this.setState('open')
        connecting.resolve()
        return
      }
      case 'command': {
        const pending = this.pending.get(parsed.request_id)
        if (pending === undefined) {
          throw new Error(`Unexpected terminal response ${parsed.request_id}`)
        }
        clearTimeout(pending.timer)
        this.pending.delete(parsed.request_id)
        if (pending.expected !== parsed.result.kind) {
          pending.reject(
            new Error(
              `Terminal command expected ${pending.expected}, received ${parsed.result.kind}`,
            ),
          )
          return
        }
        pending.resolve(parsed.result)
        return
      }
      case 'error': {
        const requestID = parsed.error.requestID
        if (requestID !== undefined) {
          const pending = this.pending.get(requestID)
          if (pending === undefined) {
            throw new Error(`Unexpected terminal error response ${requestID}`)
          }
          clearTimeout(pending.timer)
          this.pending.delete(requestID)
          pending.reject(parsed.error)
          return
        }
        this.onError?.(parsed.error)
        return
      }
      case 'stream':
        this.deliver(parsed.message)
        return
      default: {
        const exhaustive: never = parsed
        throw new Error(`Unsupported terminal message ${String(exhaustive)}`)
      }
    }
  }

  private deliver(message: TerminalMessage): void {
    this.delivery = this.delivery
      .then(async () => {
        if (this.deliveryStopped) return
        await this.onMessage(message)
        this.cursors.set(messageKey(message), message.cursor)
      })
      .catch((error) => {
        const failure = toError(error)
        this.fail(failure)
        this.reportError(failure)
        this.ws?.close()
      })
  }

  private fail(error: Error): void {
    this.deliveryStopped = true
    const connecting = this.connectPromise
    this.connectPromise = null
    connecting?.reject(error)
    for (const pending of this.pending.values()) {
      clearTimeout(pending.timer)
      pending.reject(error)
    }
    this.pending.clear()
  }

  private reportError(error: Error): void {
    try {
      this.onError?.(error)
    } catch {
      // Error observers cannot revive or interrupt a failed stream.
    }
  }

  private setState(state: ConnectionState): void {
    this.onStateChange?.(state)
  }
}

export function decimalString(value: bigint): DecimalString {
  if (value < 0n || value > MAX_UINT64) {
    throw new Error('Decimal value is outside uint64 range')
  }
  const encoded = value.toString()
  if (!isDecimalString(encoded)) {
    throw new Error('Decimal value is not canonical')
  }
  return encoded
}

function parseTextMessage(payload: string): ParsedText {
  let value: unknown
  try {
    value = JSON.parse(payload)
  } catch (error) {
    throw new Error(`Invalid terminal JSON: ${toError(error).message}`)
  }
  const object = requireRecord(value, 'terminal message')
  if (object.version !== VERSION || typeof object.type !== 'string') {
    throw new Error('Invalid terminal message version or type')
  }
  switch (object.type) {
    case 'hello':
      requireKeys(object, ['version', 'type'])
      return { kind: 'hello' }
    case 'subscribed': {
      requireKeys(
        object,
        ['version', 'type', 'request_id', 'agent_id', 'mode'],
        ['cursor'],
      )
      const agentID = requireAgentID(object.agent_id)
      const mode = requireMode(object.mode)
      const cursor =
        object.cursor === undefined ? undefined : parseCursor(object.cursor)
      if ((mode === 'snapshot') !== (cursor === undefined)) {
        throw new Error('Subscribed cursor does not match terminal mode')
      }
      return {
        kind: 'command',
        request_id: requireString(object.request_id, 'request_id'),
        result: { kind: 'subscribed', agent_id: agentID, mode, cursor },
      }
    }
    case 'unsubscribed':
      requireKeys(object, [
        'version',
        'type',
        'request_id',
        'agent_id',
        'mode',
      ])
      return {
        kind: 'command',
        request_id: requireString(object.request_id, 'request_id'),
        result: {
          kind: 'unsubscribed',
          agent_id: requireAgentID(object.agent_id),
          mode: requireMode(object.mode),
        },
      }
    case 'ack': {
      requireKeys(object, ['version', 'type', 'request_id'], ['bytes'])
      const bytes =
        object.bytes === undefined
          ? undefined
          : requireNonNegativeInteger(object.bytes, 'bytes')
      return {
        kind: 'command',
        request_id: requireString(object.request_id, 'request_id'),
        result: { kind: 'ack', bytes },
      }
    }
    case 'error':
      return parseError(object)
    case 'event':
      return { kind: 'stream', message: parseEvent(object) }
    case 'resized':
      return { kind: 'stream', message: parseResize(object) }
    case 'snapshot':
      return { kind: 'stream', message: parseSnapshot(object) }
    case 'caught_up':
      return { kind: 'stream', message: parseCaughtUp(object) }
    default:
      throw new Error(`Unsupported terminal message type ${object.type}`)
  }
}

function parseError(object: Record<string, unknown>): ParsedText {
  requireKeys(object, ['version', 'type', 'code', 'message'], [
    'request_id',
    'agent_id',
    'mode',
    'missing',
    'subscriptions',
  ])
  const missing =
    object.missing === undefined
      ? []
      : requireArray(object.missing, 'missing').map((item) => {
          const range = requireRecord(item, 'missing range')
          requireKeys(range, ['start', 'end'])
          return {
            start: requireDecimal(range.start, 'missing start'),
            end: requireDecimal(range.end, 'missing end'),
          }
        })
  const subscriptions =
    object.subscriptions === undefined
      ? []
      : requireArray(object.subscriptions, 'subscriptions').map((item) => {
          const subscription = requireRecord(item, 'resume subscription')
          requireKeys(subscription, ['agent_id', 'mode', 'cursor'])
          return {
            agent_id: requireAgentID(subscription.agent_id),
            mode: requireMode(subscription.mode),
            cursor: parseCursor(subscription.cursor),
          }
        })
  return {
    kind: 'error',
    error: new TerminalProtocolError({
      requestID: optionalString(object.request_id, 'request_id'),
      agentID:
        object.agent_id === undefined
          ? undefined
          : requireAgentID(object.agent_id),
      mode: object.mode === undefined ? undefined : requireMode(object.mode),
      code: requireString(object.code, 'code'),
      message: requireString(object.message, 'message'),
      missing,
      subscriptions,
    }),
  }
}

function parseEvent(object: Record<string, unknown>): TerminalEvent {
  requireKeys(object, [
    'version',
    'type',
    'agent_id',
    'event',
    'cursor',
    'historical',
  ])
  const eventObject = requireRecord(object.event, 'event')
  requireKeys(eventObject, ['seq', 'timestamp', 'type'], [
    'agent_id',
    'session_id',
    'from',
    'to',
    'reason',
    'payload',
  ])
  const event: TerminalEventEnvelope = {
    seq: requireDecimal(eventObject.seq, 'event seq'),
    timestamp: requireString(eventObject.timestamp, 'event timestamp'),
    type: requireString(eventObject.type, 'event type'),
  }
  assignOptionalString(event, 'agent_id', eventObject.agent_id)
  assignOptionalString(event, 'session_id', eventObject.session_id)
  assignOptionalString(event, 'from', eventObject.from)
  assignOptionalString(event, 'to', eventObject.to)
  assignOptionalString(event, 'reason', eventObject.reason)
  assignOptionalString(event, 'payload', eventObject.payload)
  const cursor = parseCursor(object.cursor)
  if (cursor.seq !== event.seq) {
    throw new Error('Event cursor does not match event sequence')
  }
  return {
    kind: 'event',
    agent_id: requireAgentID(object.agent_id),
    event,
    cursor,
    historical: requireBoolean(object.historical, 'historical'),
  }
}

function parseResize(object: Record<string, unknown>): TerminalResize {
  requireKeys(object, [
    'version',
    'type',
    'agent_id',
    'seq',
    'rows',
    'columns',
    'output_offset',
    'cursor',
    'historical',
  ])
  const sequence = requireDecimal(object.seq, 'resize seq')
  const outputOffset = requireDecimal(object.output_offset, 'resize output offset')
  const cursor = parseCursor(object.cursor)
  if (cursor.seq !== sequence || cursor.next_offset !== outputOffset) {
    throw new Error('Resize cursor does not match resize position')
  }
  return {
    kind: 'resized',
    agent_id: requireAgentID(object.agent_id),
    seq: sequence,
    rows: requireSize(object.rows, 'rows'),
    columns: requireSize(object.columns, 'columns'),
    output_offset: outputOffset,
    cursor,
    historical: requireBoolean(object.historical, 'historical'),
  }
}

function parseSnapshot(object: Record<string, unknown>): TerminalSnapshot {
  requireKeys(object, [
    'version',
    'type',
    'agent_id',
    'cursor',
    'rows',
    'columns',
    'lines',
    'truncated',
    'restorable',
    'captured_at',
  ])
  if (object.restorable !== false) {
    throw new Error('Terminal snapshots must be non-restorable')
  }
  const lines = requireArray(object.lines, 'snapshot lines')
  if (!lines.every((line) => typeof line === 'string')) {
    throw new Error('Snapshot lines must contain strings')
  }
  return {
    kind: 'snapshot',
    agent_id: requireAgentID(object.agent_id),
    cursor: parseCursor(object.cursor),
    rows: requireSize(object.rows, 'rows'),
    columns: requireSize(object.columns, 'columns'),
    lines,
    truncated: requireBoolean(object.truncated, 'truncated'),
    restorable: false,
    captured_at: requireString(object.captured_at, 'captured_at'),
  }
}

function parseCaughtUp(object: Record<string, unknown>): TerminalCaughtUp {
  requireKeys(object, ['version', 'type', 'agent_id', 'mode', 'cursor'])
  const mode = requireMode(object.mode)
  if (mode === 'snapshot') {
    throw new Error('Snapshot subscription cannot emit caught_up')
  }
  return {
    kind: 'caught_up',
    agent_id: requireAgentID(object.agent_id),
    mode,
    cursor: parseCursor(object.cursor),
  }
}

function parseRawFrame(buffer: ArrayBuffer): TerminalOutput {
  const bytes = new Uint8Array(buffer)
  if (bytes.byteLength <= RAW_HEADER_BYTES) {
    throw new Error('Terminal raw frame is truncated')
  }
  if (
    bytes[0] !== 0x44 ||
    bytes[1] !== 0x52 ||
    bytes[2] !== 0x56 ||
    bytes[3] !== 0x32
  ) {
    throw new Error('Terminal raw frame has invalid magic')
  }
  if (bytes[4] !== RAW_OUTPUT_KIND) {
    throw new Error(`Unsupported terminal raw kind ${bytes[4]}`)
  }
  if ((bytes[5] & ~HISTORICAL_FLAG) !== 0) {
    throw new Error(`Unsupported terminal raw flags ${bytes[5]}`)
  }
  const view = new DataView(buffer)
  const agentLength = view.getUint16(6, false)
  if (
    agentLength === 0 ||
    agentLength > MAX_ID_BYTES ||
    bytes.byteLength <= RAW_HEADER_BYTES + agentLength
  ) {
    throw new Error('Terminal raw frame has invalid agent length')
  }
  const agentBytes = bytes.slice(24, 24 + agentLength)
  if (!agentBytes.every((value) => value <= 0x7f)) {
    throw new Error('Terminal raw agent ID is not ASCII')
  }
  const agentID = String.fromCharCode(...agentBytes)
  validateAgentID(agentID)
  const data = bytes.slice(24 + agentLength)
  if (data.byteLength > MAX_OUTPUT_BYTES) {
    throw new Error('Terminal raw frame exceeds output chunk limit')
  }
  const sequence = view.getBigUint64(8, false)
  const offset = view.getBigUint64(16, false)
  const nextOffset = offset + BigInt(data.byteLength)
  if (nextOffset > MAX_UINT64) {
    throw new Error('Terminal raw frame cursor overflows')
  }
  const seq = decimalString(sequence)
  const start = decimalString(offset)
  const cursor = { seq, next_offset: decimalString(nextOffset) }
  validateCursor(cursor)
  return {
    kind: 'output',
    agent_id: agentID,
    seq,
    offset: start,
    data,
    historical: (bytes[5] & HISTORICAL_FLAG) !== 0,
    cursor,
  }
}

function validateSubscription(subscription: TerminalSubscription): void {
  validateAgentID(subscription.agent_id)
  if ('start' in subscription && subscription.start !== undefined) {
    switch (subscription.start.kind) {
      case 'cursor':
        validateCursor(subscription.start.cursor)
        break
      case 'sequence':
        requireDecimal(subscription.start.seq, 'sequence')
        break
      case 'offset':
        requireDecimal(subscription.start.offset, 'offset')
        break
      default: {
        const exhaustive: never = subscription.start
        throw new Error(`Unsupported terminal start ${String(exhaustive)}`)
      }
    }
  }
  if (
    subscription.mode === 'raw' &&
    subscription.writable === true &&
    subscription.viewport !== undefined
  ) {
    validateSize(subscription.viewport.rows, subscription.viewport.columns)
  }
}

function parseCursor(value: unknown): TerminalCursor {
  const cursor = requireRecord(value, 'cursor')
  requireKeys(cursor, ['seq', 'next_offset'])
  const parsed = {
    seq: requireDecimal(cursor.seq, 'cursor seq'),
    next_offset: requireDecimal(cursor.next_offset, 'cursor next_offset'),
  }
  validateCursor(parsed)
  return parsed
}

function validateCursor(cursor: TerminalCursor): void {
  if (cursor.seq === '0' && cursor.next_offset !== '0') {
    throw new Error('Origin cursor requires zero next_offset')
  }
}

function messageKey(message: TerminalMessage): string {
  switch (message.kind) {
    case 'output':
    case 'resized':
      return subscriptionKey(message.agent_id, 'raw')
    case 'event':
      return subscriptionKey(message.agent_id, 'events')
    case 'snapshot':
      return subscriptionKey(message.agent_id, 'snapshot')
    case 'caught_up':
      return subscriptionKey(message.agent_id, message.mode)
    default: {
      const exhaustive: never = message
      throw new Error(`Unsupported terminal message ${String(exhaustive)}`)
    }
  }
}

function subscriptionKey(agentID: string, mode: TerminalMode): string {
  return `${agentID}\u0000${mode}`
}

function requireRecord(value: unknown, name: string): Record<string, unknown> {
  if (!isRecord(value)) {
    throw new Error(`${name} must be an object`)
  }
  return value
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function requireKeys(
  value: Record<string, unknown>,
  required: string[],
  optional: string[] = [],
): void {
  const allowed = new Set([...required, ...optional])
  for (const key of Object.keys(value)) {
    if (!allowed.has(key)) {
      throw new Error(`Unexpected field ${key}`)
    }
  }
  for (const key of required) {
    if (!(key in value)) {
      throw new Error(`Missing field ${key}`)
    }
  }
}

function requireArray(value: unknown, name: string): unknown[] {
  if (!Array.isArray(value)) {
    throw new Error(`${name} must be an array`)
  }
  return value
}

function requireString(value: unknown, name: string): string {
  if (typeof value !== 'string' || value.length === 0) {
    throw new Error(`${name} must be a non-empty string`)
  }
  return value
}

function optionalString(value: unknown, name: string): string | undefined {
  if (value === undefined) return undefined
  return requireString(value, name)
}

function requireBoolean(value: unknown, name: string): boolean {
  if (typeof value !== 'boolean') {
    throw new Error(`${name} must be boolean`)
  }
  return value
}

function requireNonNegativeInteger(value: unknown, name: string): number {
  if (typeof value !== 'number' || !Number.isSafeInteger(value) || value < 0) {
    throw new Error(`${name} must be a non-negative integer`)
  }
  return value
}

function requireSize(value: unknown, name: string): number {
  const size = requireNonNegativeInteger(value, name)
  if (size === 0 || size > 65_535) {
    throw new Error(`${name} must be in 1..65535`)
  }
  return size
}

function requireAgentID(value: unknown): string {
  const agentID = requireString(value, 'agent_id')
  validateAgentID(agentID)
  return agentID
}

function validateAgentID(value: string): void {
  if (
    value.length === 0 ||
    value.length > MAX_ID_BYTES ||
    !/^[A-Za-z0-9_.:-]+$/.test(value)
  ) {
    throw new Error('agent_id is invalid')
  }
}

function requireMode(value: unknown): TerminalMode {
  if (value === 'raw' || value === 'events' || value === 'snapshot') {
    return value
  }
  throw new Error('Terminal mode is invalid')
}

function validateMode(mode: TerminalMode): void {
  requireMode(mode)
}

function validateSize(rows: number, columns: number): void {
  requireSize(rows, 'rows')
  requireSize(columns, 'columns')
}

function requireDecimal(value: unknown, name: string): DecimalString {
  if (!isDecimalString(value)) {
    throw new Error(`${name} must be a canonical uint64 decimal string`)
  }
  return value
}

function isDecimalString(value: unknown): value is DecimalString {
  if (typeof value !== 'string' || !/^(0|[1-9][0-9]*)$/.test(value)) {
    return false
  }
  try {
    return BigInt(value) <= MAX_UINT64
  } catch {
    return false
  }
}

function assignOptionalString(
  target: TerminalEventEnvelope,
  key: 'agent_id' | 'session_id' | 'from' | 'to' | 'reason' | 'payload',
  value: unknown,
): void {
  if (value !== undefined) {
    target[key] = requireString(value, key)
  }
}

function toError(value: unknown): Error {
  return value instanceof Error ? value : new Error(String(value))
}
