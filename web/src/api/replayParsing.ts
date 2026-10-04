import {
  optionalString,
  parseCursor,
  parseOutputRange,
  requireArray,
  requireBoolean,
  requireKeys,
  requireNonNegativeInteger,
  requirePositiveInteger,
  requireRecord,
  requireSize,
  requireString,
  requireText,
  requireTimestamp,
} from './parsing'
import { parseAgentState } from './resourceParsing'
import type {
  BlockedOccurrence,
  OutputCoverage,
  OutputRange,
  TerminalFramePreview,
  TerminalTimeline,
  TimelineStateSpan,
} from './types'

export interface ParsedOutputExpiry {
  readonly sessionID: string
  readonly missing: ReadonlyArray<OutputRange>
}

export function parseTimeline(value: unknown): TerminalTimeline {
  const object = requireRecord(value, 'timeline')
  requireKeys(object, [
    'session_id',
    'agent_id',
    'captured',
    'captured_at',
    'duration_ms',
    'output',
    'spans',
    'blocked',
  ])
  const captured = parseCursor(object.captured, 'timeline.captured')
  const output = parseOutputCoverage(object.output)
  if (output.range.start !== 0n || output.range.end !== captured.nextOffset) {
    throw new Error('timeline output range does not match captured cursor')
  }
  const spans = requireArray(object.spans, 'timeline.spans').map((span, index) =>
    parseStateSpan(span, `timeline.spans[${index}]`),
  )
  const blocked = requireArray(object.blocked, 'timeline.blocked').map(
    (occurrence, index) =>
      parseBlockedOccurrence(
        occurrence,
        `timeline.blocked[${index}]`,
        index + 1,
      ),
  )
  return {
    sessionID: requireString(object.session_id, 'timeline.session_id'),
    agentID: requireString(object.agent_id, 'timeline.agent_id'),
    captured,
    capturedAt: requireTimestamp(object.captured_at, 'timeline.captured_at'),
    durationMillis: requireNonNegativeInteger(
      object.duration_ms,
      'timeline.duration_ms',
    ),
    output,
    spans,
    blocked,
  }
}

export function parseFramePreview(value: unknown): TerminalFramePreview {
  const object = requireRecord(value, 'frame')
  requireKeys(object, [
    'session_id',
    'cursor',
    'rows',
    'columns',
    'lines',
    'truncated',
    'fidelity',
    'restorable',
  ])
  if (object.fidelity !== 'exact_origin_replay') {
    throw new Error('frame fidelity is unsupported')
  }
  if (object.restorable !== false) {
    throw new Error('frame previews must be non-restorable')
  }
  const lines = requireArray(object.lines, 'frame.lines').map((line, index) =>
    requireText(line, `frame.lines[${index}]`),
  )
  return {
    kind: 'frame_preview',
    sessionID: requireString(object.session_id, 'frame.session_id'),
    cursor: parseCursor(object.cursor, 'frame.cursor'),
    rows: requireSize(object.rows, 'frame.rows'),
    columns: requireSize(object.columns, 'frame.columns'),
    lines,
    truncated: requireBoolean(object.truncated, 'frame.truncated'),
    fidelity: 'exact_origin_replay',
    restorable: false,
  }
}

export function parseOutputExpiry(value: unknown): ParsedOutputExpiry {
  const object = requireRecord(value, 'output expiry')
  requireKeys(object, ['error', 'code', 'session_id', 'missing'])
  requireString(object.error, 'output expiry.error')
  if (object.code !== 'output_expired') {
    throw new Error('HTTP 410 response has an unsupported code')
  }
  const missing = requireArray(object.missing, 'output expiry.missing').map(
    (range, index) =>
      parseOutputRange(range, `output expiry.missing[${index}]`),
  )
  if (missing.length === 0 || missing.some((range) => range.start === range.end)) {
    throw new Error('output expiry must contain non-empty missing ranges')
  }
  return {
    sessionID: requireString(object.session_id, 'output expiry.session_id'),
    missing,
  }
}

function parseStateSpan(value: unknown, name: string): TimelineStateSpan {
  const object = requireRecord(value, name)
  requireKeys(object, ['state', 'start', 'start_at'], [
    'end',
    'end_at',
    'duration_ms',
    'source',
    'rule',
    'reason',
  ])
  const start = parseCursor(object.start, `${name}.start`)
  const startAt = requireTimestamp(object.start_at, `${name}.start_at`)
  const hasEnd = object.end !== undefined
  if (
    hasEnd !== (object.end_at !== undefined) ||
    hasEnd !== (object.duration_ms !== undefined)
  ) {
    throw new Error(`${name} must provide end, end_at, and duration_ms together`)
  }
  const end = hasEnd ? parseCursor(object.end, `${name}.end`) : undefined
  const endAt = hasEnd
    ? requireTimestamp(object.end_at, `${name}.end_at`)
    : undefined
  const durationMillis = hasEnd
    ? requireNonNegativeInteger(object.duration_ms, `${name}.duration_ms`)
    : undefined
  if (
    end !== undefined &&
    (end.seq < start.seq || end.nextOffset < start.nextOffset)
  ) {
    throw new Error(`${name}.end precedes start`)
  }
  if (endAt !== undefined && endAt.epochMillis < startAt.epochMillis) {
    throw new Error(`${name}.end_at precedes start_at`)
  }
  return {
    state: parseAgentState(object.state, `${name}.state`),
    start,
    startAt,
    ...(end === undefined ? {} : { end }),
    ...(endAt === undefined ? {} : { endAt }),
    ...(durationMillis === undefined ? {} : { durationMillis }),
    ...optionalNamedStrings(object, name),
  }
}

function parseBlockedOccurrence(
  value: unknown,
  name: string,
  expectedNumber: number,
): BlockedOccurrence {
  const object = requireRecord(value, name)
  requireKeys(object, ['number', 'span', 'jump', 'frame_available'])
  const number = requirePositiveInteger(object.number, `${name}.number`)
  if (number !== expectedNumber) {
    throw new Error(`${name}.number is not sequential`)
  }
  const span = parseStateSpan(object.span, `${name}.span`)
  if (span.state !== 'blocked') {
    throw new Error(`${name}.span must be blocked`)
  }
  return {
    number,
    span,
    jump: parseCursor(object.jump, `${name}.jump`),
    frameAvailable: requireBoolean(
      object.frame_available,
      `${name}.frame_available`,
    ),
  }
}

function parseOutputCoverage(value: unknown): OutputCoverage {
  const object = requireRecord(value, 'timeline.output')
  requireKeys(object, ['range', 'retained', 'missing'])
  const range = parseOutputRange(object.range, 'timeline.output.range')
  const retained = requireArray(object.retained, 'timeline.output.retained').map(
    (entry, index) =>
      parseOutputRange(entry, `timeline.output.retained[${index}]`),
  )
  const missing = requireArray(object.missing, 'timeline.output.missing').map(
    (entry, index) =>
      parseOutputRange(entry, `timeline.output.missing[${index}]`),
  )
  validateSubranges(range, retained, 'timeline.output.retained')
  validateSubranges(range, missing, 'timeline.output.missing')
  validateCoveragePartition(range, retained, missing)
  return { range, retained, missing }
}

function validateSubranges(
  full: OutputRange,
  ranges: ReadonlyArray<OutputRange>,
  name: string,
): void {
  let previousEnd = full.start
  for (const [index, range] of ranges.entries()) {
    if (
      range.start < full.start ||
      range.end > full.end ||
      range.start >= range.end ||
      range.start < previousEnd
    ) {
      throw new Error(`${name}[${index}] is outside or overlaps its range`)
    }
    previousEnd = range.end
  }
}

function validateCoveragePartition(
  full: OutputRange,
  retained: ReadonlyArray<OutputRange>,
  missing: ReadonlyArray<OutputRange>,
): void {
  const partition = [...retained, ...missing].sort((left, right) =>
    left.start < right.start ? -1 : left.start > right.start ? 1 : 0,
  )
  let nextOffset = full.start
  for (const range of partition) {
    if (range.start !== nextOffset) {
      throw new Error('timeline output coverage has a gap or overlap')
    }
    nextOffset = range.end
  }
  if (nextOffset !== full.end) {
    throw new Error('timeline output coverage does not cover its full range')
  }
}

function optionalNamedStrings(
  object: Record<string, unknown>,
  name: string,
): Pick<TimelineStateSpan, 'source' | 'rule' | 'reason'> {
  const source = optionalString(object.source, `${name}.source`)
  const rule = optionalString(object.rule, `${name}.rule`)
  const reason = optionalString(object.reason, `${name}.reason`)
  return {
    ...(source === undefined ? {} : { source }),
    ...(rule === undefined ? {} : { rule }),
    ...(reason === undefined ? {} : { reason }),
  }
}
