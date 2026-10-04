import type {
  OutputRange,
  RecordingCursor,
  Timestamp,
} from '../api/types'
import type {
  RecordedOperation,
  RecordedTimestamp,
} from '../terminal/sessionTape'

export function recordingCursor(
  seq: bigint,
  nextOffset: bigint,
): RecordingCursor {
  return { seq, nextOffset }
}

export function outputOperation(options: {
  seq: bigint
  offset: bigint
  data: ReadonlyArray<number>
}): Extract<RecordedOperation, { readonly kind: 'output' }> {
  const bytes = Uint8Array.from(options.data)
  return {
    kind: 'output',
    seq: options.seq,
    offset: options.offset,
    data: bytes,
    cursor: recordingCursor(
      options.seq,
      options.offset + BigInt(bytes.byteLength),
    ),
  }
}

export function resizeOperation(options: {
  seq: bigint
  nextOffset: bigint
  rows?: number
  columns?: number
}): Extract<RecordedOperation, { readonly kind: 'resize' }> {
  return {
    kind: 'resize',
    seq: options.seq,
    rows: options.rows ?? 40,
    columns: options.columns ?? 120,
    cursor: recordingCursor(options.seq, options.nextOffset),
  }
}

export function recordedTimestamp(
  seq: bigint,
  atMillis: number,
): RecordedTimestamp {
  return { seq, atMillis }
}

export function timestamp(
  iso = '2026-10-04T12:00:00Z',
  epochMillis = 1_791_115_200_000,
): Timestamp {
  return { iso, epochMillis }
}

export function outputRange(start: bigint, end: bigint): OutputRange {
  return { start, end }
}

export function timelineWire(): Record<string, unknown> {
  const openBlocked = stateSpanWire({
    state: 'blocked',
    startSeq: '4',
    offset: '5',
    startAt: '2026-10-04T12:00:05Z',
  })
  return {
    session_id: 'agent-1',
    agent_id: 'agent-1',
    captured: { seq: '5', next_offset: '5' },
    captured_at: '2026-10-04T12:00:40Z',
    duration_ms: 40_000,
    output: {
      range: { start: '0', end: '5' },
      retained: [{ start: '0', end: '5' }],
      missing: [],
    },
    spans: [openBlocked],
    blocked: [
      {
        number: 1,
        span: openBlocked,
        jump: { seq: '3', next_offset: '5' },
        frame_available: true,
      },
    ],
  }
}

export function frameWire(): Record<string, unknown> {
  return {
    session_id: 'agent-1',
    cursor: { seq: '4', next_offset: '5' },
    rows: 40,
    columns: 120,
    lines: ['hello'],
    truncated: false,
    fidelity: 'exact_origin_replay',
    restorable: false,
  }
}

function stateSpanWire(options: {
  state: string
  startSeq: string
  offset: string
  startAt: string
}): Record<string, unknown> {
  return {
    state: options.state,
    start: { seq: options.startSeq, next_offset: options.offset },
    start_at: options.startAt,
  }
}
