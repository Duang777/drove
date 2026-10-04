import { parseCursor, parseUint64 } from '../api/parsing'
import type {
  TerminalEvent,
  TerminalOutput,
  TerminalResize,
} from '../api/types'
import type {
  RecordedOperation,
  RecordedTimestamp,
} from './sessionTape'

export function recordedOperationFromMessage(
  message: TerminalOutput | TerminalResize,
): RecordedOperation {
  const cursor = parseCursor(message.cursor, `${message.kind}.cursor`)
  switch (message.kind) {
    case 'output':
      return {
        kind: 'output',
        seq: parseUint64(message.seq, 'output.seq'),
        offset: parseUint64(message.offset, 'output.offset'),
        data: message.data,
        cursor,
      }
    case 'resized':
      return {
        kind: 'resize',
        seq: parseUint64(message.seq, 'resize.seq'),
        rows: message.rows,
        columns: message.columns,
        cursor,
      }
    default: {
      const exhaustive: never = message
      throw new Error(`Unsupported recording message ${String(exhaustive)}`)
    }
  }
}

export function recordedTimestampFromMessage(
  message: TerminalEvent,
): RecordedTimestamp {
  return {
    seq: parseUint64(message.event.seq, 'event.seq'),
    atMillis: message.event.timestamp.epochMillis,
  }
}
