import type { OutputRange, RecordingCursor } from '../api/types'

export const DEFAULT_TAPE_BYTE_LIMIT = 64 * 1024 * 1024

const MAX_UINT64 = 18_446_744_073_709_551_615n
const ORIGIN: RecordingCursor = Object.freeze({ seq: 0n, nextOffset: 0n })

export type RecordedOperation =
  | {
      readonly kind: 'output'
      readonly seq: bigint
      readonly offset: bigint
      readonly data: Uint8Array
      readonly cursor: RecordingCursor
    }
  | {
      readonly kind: 'resize'
      readonly seq: bigint
      readonly rows: number
      readonly columns: number
      readonly cursor: RecordingCursor
    }

export interface RecordedTimestamp {
  readonly seq: bigint
  readonly atMillis: number
}

export interface TimedOperation {
  readonly operation: RecordedOperation
  readonly atMillis: number
}

export type ReplayPlan =
  | {
      readonly kind: 'exact'
      readonly target: RecordingCursor
      readonly operations: ReadonlyArray<TimedOperation>
    }
  | { readonly kind: 'not_ready' }
  | { readonly kind: 'expired'; readonly missing: ReadonlyArray<OutputRange> }
  | { readonly kind: 'local_limit'; readonly byteLimit: number }

export type TapeRejection =
  | 'conflicting_duplicate'
  | 'invalid_operation'
  | 'output_gap'
  | 'sequence_regression'
  | 'time_regression'

export type TapeRecordResult =
  | {
      readonly kind: 'accepted'
      readonly exactFrontier: RecordingCursor
    }
  | {
      readonly kind: 'duplicate'
      readonly exactFrontier: RecordingCursor
    }
  | { readonly kind: 'local_limit'; readonly byteLimit: number }
  | { readonly kind: 'rejected'; readonly reason: TapeRejection }

interface SessionTapeOptions {
  readonly byteLimit?: number
}

interface ReplayRequest {
  readonly target: RecordingCursor
  readonly missing?: ReadonlyArray<OutputRange>
}

interface ReplayAtRequest {
  readonly atMillis: number
  readonly missing?: ReadonlyArray<OutputRange>
}

/**
 * SessionTape keeps the browser's exact, origin-based terminal prefix.
 *
 * Raw operations and event timestamps arrive independently. A recorded
 * operation becomes replayable only after both copies of its sequence exist.
 */
export class SessionTape {
  private readonly byteLimit: number
  private readonly operations: RecordedOperation[] = []
  private readonly operationBySeq = new Map<bigint, RecordedOperation>()
  private readonly timestampBySeq = new Map<bigint, number>()
  private exactCount = 0
  private outputOffset = 0n
  private byteCount = 0
  private frozen = false
  private firstLimitedSeq: bigint | undefined
  private lastTimestampSeq = 0n
  private lastTimestampMillis: number | undefined

  constructor(options: SessionTapeOptions = {}) {
    const byteLimit = options.byteLimit ?? DEFAULT_TAPE_BYTE_LIMIT
    if (!Number.isSafeInteger(byteLimit) || byteLimit <= 0) {
      throw new Error('SessionTape byte limit must be a positive safe integer')
    }
    this.byteLimit = byteLimit
  }

  recordOperation(operation: RecordedOperation): TapeRecordResult {
    const existing = this.operationBySeq.get(operation.seq)
    if (existing !== undefined) {
      return operationsEqual(existing, operation)
        ? this.result('duplicate')
        : { kind: 'rejected', reason: 'conflicting_duplicate' }
    }
    if (this.frozen) {
      return { kind: 'local_limit', byteLimit: this.byteLimit }
    }

    const last = this.operations.at(-1)
    if (last !== undefined && operation.seq < last.seq) {
      return { kind: 'rejected', reason: 'sequence_regression' }
    }
    const invalid = validateOperation(operation, this.outputOffset)
    if (invalid !== undefined) {
      return { kind: 'rejected', reason: invalid }
    }

    const addedBytes =
      operation.kind === 'output' ? operation.data.byteLength : 0
    if (addedBytes > this.byteLimit - this.byteCount) {
      this.frozen = true
      this.firstLimitedSeq = operation.seq
      return { kind: 'local_limit', byteLimit: this.byteLimit }
    }

    const stored = cloneOperation(operation)
    this.operations.push(stored)
    this.operationBySeq.set(stored.seq, stored)
    this.outputOffset = stored.cursor.nextOffset
    this.byteCount += addedBytes
    this.advanceExactFrontier()
    return this.result('accepted')
  }

  recordTimestamp(timestamp: RecordedTimestamp): TapeRecordResult {
    const existing = this.timestampBySeq.get(timestamp.seq)
    if (existing !== undefined) {
      return existing === timestamp.atMillis
        ? this.result('duplicate')
        : { kind: 'rejected', reason: 'conflicting_duplicate' }
    }
    if (
      timestamp.seq <= 0n ||
      timestamp.seq > MAX_UINT64 ||
      !Number.isSafeInteger(timestamp.atMillis)
    ) {
      return { kind: 'rejected', reason: 'invalid_operation' }
    }
    if (timestamp.seq < this.lastTimestampSeq) {
      return { kind: 'rejected', reason: 'sequence_regression' }
    }
    if (
      this.lastTimestampMillis !== undefined &&
      timestamp.atMillis < this.lastTimestampMillis
    ) {
      return { kind: 'rejected', reason: 'time_regression' }
    }

    this.timestampBySeq.set(timestamp.seq, timestamp.atMillis)
    this.lastTimestampSeq = timestamp.seq
    this.lastTimestampMillis = timestamp.atMillis
    this.advanceExactFrontier()
    return this.result('accepted')
  }

  plan(request: ReplayRequest): ReplayPlan {
    const missing = missingBefore(request.missing ?? [], request.target.nextOffset)
    if (missing.length > 0) {
      return { kind: 'expired', missing }
    }
    if (cursorEqual(request.target, ORIGIN)) {
      return { kind: 'exact', target: ORIGIN, operations: [] }
    }

    for (let index = 0; index < this.exactCount; index += 1) {
      const operation = this.operations[index]
      if (operation === undefined) break
      if (cursorEqual(operation.cursor, request.target)) {
        return {
          kind: 'exact',
          target: cloneCursor(request.target),
          operations: this.timedPrefix(index + 1),
        }
      }
      if (operation.seq > request.target.seq) break
    }

    if (this.frozen && cursorAfter(request.target, this.lastRecordedCursor())) {
      return { kind: 'local_limit', byteLimit: this.byteLimit }
    }
    return { kind: 'not_ready' }
  }

  planAt(request: ReplayAtRequest): ReplayPlan {
    if (!Number.isSafeInteger(request.atMillis)) {
      return { kind: 'not_ready' }
    }
    if (
      this.lastTimestampMillis === undefined ||
      request.atMillis > this.lastTimestampMillis
    ) {
      return this.frozen
        ? { kind: 'local_limit', byteLimit: this.byteLimit }
        : { kind: 'not_ready' }
    }
    if (this.timeExceedsLocalPrefix(request.atMillis)) {
      return { kind: 'local_limit', byteLimit: this.byteLimit }
    }
    let target = ORIGIN
    for (let index = 0; index < this.exactCount; index += 1) {
      const operation = this.operations[index]
      if (operation === undefined) break
      const atMillis = this.timestampBySeq.get(operation.seq)
      if (atMillis === undefined || atMillis > request.atMillis) break
      target = operation.cursor
    }
    return this.plan({ target, missing: request.missing })
  }

  exactFrontier(): RecordingCursor {
    if (this.exactCount === 0) return ORIGIN
    const operation = this.operations[this.exactCount - 1]
    return operation === undefined ? ORIGIN : cloneCursor(operation.cursor)
  }

  storedBytes(): number {
    return this.byteCount
  }

  reachedLocalLimit(): boolean {
    return this.frozen
  }

  private advanceExactFrontier(): void {
    while (this.exactCount < this.operations.length) {
      const operation = this.operations[this.exactCount]
      if (
        operation === undefined ||
        !this.timestampBySeq.has(operation.seq)
      ) {
        return
      }
      this.exactCount += 1
    }
  }

  private lastRecordedCursor(): RecordingCursor {
    const operation = this.operations.at(-1)
    return operation === undefined ? ORIGIN : operation.cursor
  }

  private timeExceedsLocalPrefix(atMillis: number): boolean {
    if (!this.frozen) return false
    const limitedAt =
      this.firstLimitedSeq === undefined
        ? undefined
        : this.timestampBySeq.get(this.firstLimitedSeq)
    if (limitedAt !== undefined) return atMillis >= limitedAt
    if (this.exactCount === 0) return false
    const operation = this.operations[this.exactCount - 1]
    if (operation === undefined) return true
    const frontierAt = this.timestampBySeq.get(operation.seq)
    return frontierAt === undefined || atMillis > frontierAt
  }

  private timedPrefix(count: number): TimedOperation[] {
    const result: TimedOperation[] = []
    for (let index = 0; index < count; index += 1) {
      const operation = this.operations[index]
      if (operation === undefined) break
      const atMillis = this.timestampBySeq.get(operation.seq)
      if (atMillis === undefined) break
      result.push({ operation: cloneOperation(operation), atMillis })
    }
    return result
  }

  private result(kind: 'accepted' | 'duplicate'): TapeRecordResult {
    return { kind, exactFrontier: this.exactFrontier() }
  }
}

function validateOperation(
  operation: RecordedOperation,
  expectedOffset: bigint,
): TapeRejection | undefined {
  if (
    operation.seq <= 0n ||
    operation.seq > MAX_UINT64 ||
    operation.cursor.seq !== operation.seq ||
    operation.cursor.nextOffset < 0n ||
    operation.cursor.nextOffset > MAX_UINT64
  ) {
    return 'invalid_operation'
  }

  switch (operation.kind) {
    case 'output': {
      if (operation.data.byteLength === 0 || operation.offset !== expectedOffset) {
        return 'output_gap'
      }
      const nextOffset = operation.offset + BigInt(operation.data.byteLength)
      if (
        operation.offset < 0n ||
        operation.offset > MAX_UINT64 ||
        nextOffset > MAX_UINT64 ||
        operation.cursor.nextOffset !== nextOffset
      ) {
        return 'invalid_operation'
      }
      return undefined
    }
    case 'resize':
      if (
        !validDimension(operation.rows) ||
        !validDimension(operation.columns) ||
        operation.cursor.nextOffset !== expectedOffset
      ) {
        return 'invalid_operation'
      }
      return undefined
    default: {
      const exhaustive: never = operation
      throw new Error(`Unsupported recorded operation ${String(exhaustive)}`)
    }
  }
}

function validDimension(value: number): boolean {
  return Number.isSafeInteger(value) && value > 0 && value <= 65_535
}

function operationsEqual(
  left: RecordedOperation,
  right: RecordedOperation,
): boolean {
  if (
    left.kind !== right.kind ||
    left.seq !== right.seq ||
    !cursorEqual(left.cursor, right.cursor)
  ) {
    return false
  }
  if (left.kind === 'resize' && right.kind === 'resize') {
    return left.rows === right.rows && left.columns === right.columns
  }
  if (left.kind === 'output' && right.kind === 'output') {
    return (
      left.offset === right.offset &&
      bytesEqual(left.data, right.data)
    )
  }
  return false
}

function bytesEqual(left: Uint8Array, right: Uint8Array): boolean {
  if (left.byteLength !== right.byteLength) return false
  return left.every((value, index) => value === right[index])
}

function cloneOperation(operation: RecordedOperation): RecordedOperation {
  switch (operation.kind) {
    case 'output':
      return {
        kind: 'output',
        seq: operation.seq,
        offset: operation.offset,
        data: operation.data.slice(),
        cursor: cloneCursor(operation.cursor),
      }
    case 'resize':
      return {
        kind: 'resize',
        seq: operation.seq,
        rows: operation.rows,
        columns: operation.columns,
        cursor: cloneCursor(operation.cursor),
      }
    default: {
      const exhaustive: never = operation
      throw new Error(`Unsupported recorded operation ${String(exhaustive)}`)
    }
  }
}

function cloneCursor(cursor: RecordingCursor): RecordingCursor {
  return { seq: cursor.seq, nextOffset: cursor.nextOffset }
}

function cursorEqual(
  left: RecordingCursor,
  right: RecordingCursor,
): boolean {
  return left.seq === right.seq && left.nextOffset === right.nextOffset
}

function cursorAfter(
  left: RecordingCursor,
  right: RecordingCursor,
): boolean {
  return left.seq > right.seq || left.nextOffset > right.nextOffset
}

function missingBefore(
  ranges: ReadonlyArray<OutputRange>,
  nextOffset: bigint,
): OutputRange[] {
  return ranges
    .filter((range) => range.start < nextOffset && range.end > 0n)
    .map((range) => ({ start: range.start, end: range.end }))
}
