import { describe, expect, it } from 'vitest'
import { parseTerminalTextMessage } from './terminalStream'

describe('terminal v2 text parsing', () => {
  it('parses event timestamps before exposing the message', () => {
    const parsed = parseTerminalTextMessage(
      JSON.stringify({
        version: 2,
        type: 'event',
        agent_id: 'agent-1',
        event: {
          seq: '2',
          timestamp: '2026-10-04T12:00:00.123456789Z',
          type: 'output.chunk',
        },
        cursor: { seq: '2', next_offset: '3' },
        historical: true,
      }),
    )

    expect(parsed).toEqual({
      kind: 'stream',
      message: {
        kind: 'event',
        agent_id: 'agent-1',
        event: {
          seq: '2',
          timestamp: {
            iso: '2026-10-04T12:00:00.123456789Z',
            epochMillis: 1_791_115_200_123,
          },
          type: 'output.chunk',
        },
        cursor: { seq: '2', next_offset: '3' },
        historical: true,
      },
    })
  })

  it.each([
    ['noncanonical sequence', '02', '2026-10-04T12:00:00Z'],
    ['invalid timestamp', '2', '2026-02-30T12:00:00Z'],
  ])('rejects %s', (_name, seq, timestamp) => {
    expect(() =>
      parseTerminalTextMessage(
        JSON.stringify({
          version: 2,
          type: 'event',
          agent_id: 'agent-1',
          event: { seq, timestamp, type: 'output.chunk' },
          cursor: { seq, next_offset: '3' },
          historical: true,
        }),
      ),
    ).toThrow()
  })

  it('rejects restorable snapshots', () => {
    expect(() =>
      parseTerminalTextMessage(
        JSON.stringify({
          version: 2,
          type: 'snapshot',
          agent_id: 'agent-1',
          cursor: { seq: '2', next_offset: '3' },
          rows: 40,
          columns: 120,
          lines: ['hello'],
          truncated: false,
          restorable: true,
          captured_at: '2026-10-04T12:00:00Z',
        }),
      ),
    ).toThrow(/non-restorable/)
  })
})
