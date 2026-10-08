import { describe, expect, it } from 'vitest'
import { parseActionContext, parseActionResponse } from './actionParsing'

describe('remote action REST parsing', () => {
  it('parses a live bounded screen and decimal-safe action sequences', () => {
    expect(
      parseActionContext({
        state_seq: '9007199254740993',
        actions: ['approve', 'deny', 'reply'],
        tickets: [
          { action: 'approve', ticket: 'approve-ticket' },
          { action: 'deny', ticket: 'deny-ticket' },
          { action: 'reply', ticket: 'reply-ticket' },
        ],
        expires_at: '2026-10-08T18:10:00Z',
        screen: {
          captured_at: '2026-10-08T18:00:00Z',
          rows: ['Review this command?', 'Press enter to confirm'],
          truncated: false,
        },
      }),
    ).toMatchObject({
      stateSeq: '9007199254740993',
      actions: ['approve', 'deny', 'reply'],
      tickets: [
        { action: 'approve', ticket: 'approve-ticket' },
        { action: 'deny', ticket: 'deny-ticket' },
        { action: 'reply', ticket: 'reply-ticket' },
      ],
      screen: {
        capturedAt: { iso: '2026-10-08T18:00:00Z' },
        rows: ['Review this command?', 'Press enter to confirm'],
        truncated: false,
      },
    })

    expect(
      parseActionResponse({
        state_seq: '9007199254740993',
        bytes_written: 16,
        action_seq: '9007199254740994',
        input_seq: '9007199254740995',
      }),
    ).toEqual({
      stateSeq: '9007199254740993',
      bytesWritten: 16,
      actionSeq: '9007199254740994',
      inputSeq: '9007199254740995',
    })
  })

  it.each([
    {
      name: 'numeric state sequence',
      patch: { state_seq: 42 },
      message: /state_seq must be a canonical uint64 decimal string/,
    },
    {
      name: 'unknown action',
      patch: { actions: ['approve', 'launch'] },
      message: /actions\[1\] is unsupported/,
    },
    {
      name: 'duplicate action',
      patch: { actions: ['approve', 'approve'] },
      message: /actions must not contain duplicates/,
    },
    {
      name: 'ticket for a different action',
      patch: {
        tickets: [{ action: 'deny', ticket: 'deny-ticket' }],
      },
      message: /tickets must match actions/,
    },
    {
      name: 'unexpected secret field',
      patch: { endpoint: 'https://push.example.test/private' },
      message: /unexpected field endpoint/,
    },
  ])('rejects $name', ({ patch, message }) => {
    expect(() => parseActionContext({ ...contextWire(), ...patch })).toThrow(
      message,
    )
  })

  it('rejects malformed screen rows and non-adjacent audit sequences', () => {
    expect(() =>
      parseActionContext({
        ...contextWire(),
        screen: {
          captured_at: '2026-10-08T18:00:00Z',
          rows: ['ok', 42],
          truncated: false,
        },
      }),
    ).toThrow(/screen.rows\[1\] must be a string/)

    expect(() =>
      parseActionResponse({
        state_seq: '42',
        bytes_written: 1,
        action_seq: '9',
        input_seq: '11',
      }),
    ).toThrow(/input_seq must immediately follow action_seq/)
  })
})

function contextWire(): Record<string, unknown> {
  return {
    state_seq: '42',
    actions: ['approve'],
    tickets: [{ action: 'approve', ticket: 'approve-ticket' }],
    expires_at: '2026-10-08T18:10:00Z',
    screen: {
      captured_at: '2026-10-08T18:00:00Z',
      rows: ['Approve?'],
      truncated: false,
    },
  }
}
