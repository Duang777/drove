import type {
  DecimalString,
  OutputRange,
  RecordingCursor,
  Timestamp,
} from './types'

const MAX_UINT64 = 18_446_744_073_709_551_615n
const RFC3339 =
  /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.(\d{1,9}))?(Z|([+-])(\d{2}):(\d{2}))$/

export function requireRecord(
  value: unknown,
  name: string,
): Record<string, unknown> {
  if (!isRecord(value)) {
    throw new Error(`${name} must be an object`)
  }
  return value
}

export function requireKeys(
  value: Record<string, unknown>,
  required: ReadonlyArray<string>,
  optional: ReadonlyArray<string> = [],
): void {
  const allowed = new Set([...required, ...optional])
  for (const key of Object.keys(value)) {
    if (!allowed.has(key)) {
      throw new Error(`${valueName(value)} has unexpected field ${key}`)
    }
  }
  for (const key of required) {
    if (!(key in value)) {
      throw new Error(`${valueName(value)} is missing field ${key}`)
    }
  }
}

export function requireArray(value: unknown, name: string): unknown[] {
  if (!Array.isArray(value)) {
    throw new Error(`${name} must be an array`)
  }
  return value
}

export function requireString(value: unknown, name: string): string {
  if (typeof value !== 'string' || value.length === 0) {
    throw new Error(`${name} must be a non-empty string`)
  }
  return value
}

export function requireText(value: unknown, name: string): string {
  if (typeof value !== 'string') {
    throw new Error(`${name} must be a string`)
  }
  return value
}

export function optionalString(
  value: unknown,
  name: string,
): string | undefined {
  if (value === undefined) return undefined
  return requireString(value, name)
}

export function requireBoolean(value: unknown, name: string): boolean {
  if (typeof value !== 'boolean') {
    throw new Error(`${name} must be boolean`)
  }
  return value
}

export function requireNonNegativeInteger(
  value: unknown,
  name: string,
): number {
  if (typeof value !== 'number' || !Number.isSafeInteger(value) || value < 0) {
    throw new Error(`${name} must be a non-negative safe integer`)
  }
  return value
}

export function requirePositiveInteger(value: unknown, name: string): number {
  const parsed = requireNonNegativeInteger(value, name)
  if (parsed === 0) {
    throw new Error(`${name} must be positive`)
  }
  return parsed
}

export function requireSize(value: unknown, name: string): number {
  const parsed = requirePositiveInteger(value, name)
  if (parsed > 65_535) {
    throw new Error(`${name} must be in 1..65535`)
  }
  return parsed
}

export function requireDecimalString(
  value: unknown,
  name: string,
): DecimalString {
  if (!isDecimalString(value)) {
    throw new Error(`${name} must be a canonical uint64 decimal string`)
  }
  return value
}

export function parseUint64(value: unknown, name: string): bigint {
  return BigInt(requireDecimalString(value, name))
}

export function formatUint64(value: bigint, name = 'value'): DecimalString {
  if (value < 0n || value > MAX_UINT64) {
    throw new Error(`${name} is outside uint64 range`)
  }
  const encoded = value.toString()
  if (!isDecimalString(encoded)) {
    throw new Error(`${name} is not canonical`)
  }
  return encoded
}

export function parseCursor(value: unknown, name: string): RecordingCursor {
  const object = requireRecord(value, name)
  requireKeys(object, ['seq', 'next_offset'])
  const cursor = {
    seq: parseUint64(object.seq, `${name}.seq`),
    nextOffset: parseUint64(object.next_offset, `${name}.next_offset`),
  }
  if (cursor.seq === 0n && cursor.nextOffset !== 0n) {
    throw new Error(`${name} origin sequence requires zero next_offset`)
  }
  return cursor
}

export function parseOutputRange(value: unknown, name: string): OutputRange {
  const object = requireRecord(value, name)
  requireKeys(object, ['start', 'end'])
  const range = {
    start: parseUint64(object.start, `${name}.start`),
    end: parseUint64(object.end, `${name}.end`),
  }
  if (range.end < range.start) {
    throw new Error(`${name} end precedes start`)
  }
  return range
}

export function requireTimestamp(value: unknown, name: string): Timestamp {
  const iso = requireString(value, name)
  const match = RFC3339.exec(iso)
  if (match === null) {
    throw new Error(`${name} must be an RFC3339 timestamp`)
  }

  const year = Number(match[1])
  const month = Number(match[2])
  const day = Number(match[3])
  const hour = Number(match[4])
  const minute = Number(match[5])
  const second = Number(match[6])
  const fractional = match[7] ?? ''
  const zone = match[8]
  const zoneSign = match[9]
  const zoneHour = Number(match[10] ?? 0)
  const zoneMinute = Number(match[11] ?? 0)
  const millisecond = Number(fractional.padEnd(3, '0').slice(0, 3))

  if (
    month < 1 ||
    month > 12 ||
    hour > 23 ||
    minute > 59 ||
    second > 59 ||
    zoneHour > 23 ||
    zoneMinute > 59
  ) {
    throw new Error(`${name} contains an invalid date or offset`)
  }

  const local = new Date(0)
  local.setUTCHours(hour, minute, second, millisecond)
  local.setUTCFullYear(year, month - 1, day)
  if (
    local.getUTCFullYear() !== year ||
    local.getUTCMonth() !== month - 1 ||
    local.getUTCDate() !== day ||
    local.getUTCHours() !== hour ||
    local.getUTCMinutes() !== minute ||
    local.getUTCSeconds() !== second
  ) {
    throw new Error(`${name} contains an invalid calendar date`)
  }

  let offsetMinutes = 0
  if (zone !== 'Z') {
    offsetMinutes = zoneHour * 60 + zoneMinute
    if (zoneSign === '-') offsetMinutes = -offsetMinutes
  }
  const epochMillis = local.getTime() - offsetMinutes * 60_000
  if (!Number.isSafeInteger(epochMillis)) {
    throw new Error(`${name} is outside the supported timestamp range`)
  }
  return { iso, epochMillis }
}

export function toError(value: unknown): Error {
  return value instanceof Error ? value : new Error(String(value))
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
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

function valueName(value: Record<string, unknown>): string {
  const type = value.type
  return typeof type === 'string' && type.length > 0 ? type : 'object'
}
