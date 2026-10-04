import { describe, expect, it } from 'vitest'
import type { TerminalEvent, TerminalOutput } from '../api/types'
import { decimalString } from '../ws/terminalStream'
import {
  recordedOperationFromMessage,
  recordedTimestampFromMessage,
} from './recordingBoundary'

describe('terminal recording boundary', () => {
  it('converts decimal transport positions to bigint domain values', () => {
    const message: TerminalOutput = {
      kind: 'output',
      agent_id: 'agent-1',
      seq: decimalString(9_007_199_254_740_993n),
      offset: decimalString(9_007_199_254_740_995n),
      data: Uint8Array.from([1, 2]),
      historical: true,
      cursor: {
        seq: decimalString(9_007_199_254_740_993n),
        next_offset: decimalString(9_007_199_254_740_997n),
      },
    }

    expect(recordedOperationFromMessage(message)).toEqual({
      kind: 'output',
      seq: 9_007_199_254_740_993n,
      offset: 9_007_199_254_740_995n,
      data: Uint8Array.from([1, 2]),
      cursor: {
        seq: 9_007_199_254_740_993n,
        nextOffset: 9_007_199_254_740_997n,
      },
    })
  })

  it('converts validated event time without accepting the wire DTO in the tape', () => {
    const message: TerminalEvent = {
      kind: 'event',
      agent_id: 'agent-1',
      event: {
        seq: decimalString(4n),
        timestamp: {
          iso: '2026-10-04T12:00:00Z',
          epochMillis: 1_791_115_200_000,
        },
        type: 'output.chunk',
      },
      cursor: {
        seq: decimalString(4n),
        next_offset: decimalString(2n),
      },
      historical: true,
    }

    expect(recordedTimestampFromMessage(message)).toEqual({
      seq: 4n,
      atMillis: 1_791_115_200_000,
    })
  })
})
