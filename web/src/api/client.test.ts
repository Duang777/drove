import { afterEach, describe, expect, it, vi } from 'vitest'
import {
  executeAgentAction,
  getAgentActionContext,
  getAgentFrame,
  listAgents,
  OutputExpiredError,
  parseFramePreview,
  parseTimeline,
} from './client'
import { frameWire, timelineWire } from '../test/terminalFixtures'
import { formatUint64 } from './parsing'
import { parseAgentStatus } from './resourceParsing'

describe('terminal REST parsing', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('converts canonical cursor and range decimals to bigint', () => {
    const value = timelineWire()
    value.captured = {
      seq: '18446744073709551615',
      next_offset: '5',
    }

    const timeline = parseTimeline(value)

    expect(timeline.captured).toEqual({
      seq: 18_446_744_073_709_551_615n,
      nextOffset: 5n,
    })
    expect(timeline.output.range).toEqual({ start: 0n, end: 5n })
    expect(timeline.blocked[0]?.span.state).toBe('blocked')
    expect(timeline.capturedAt.epochMillis).toBe(1_791_115_240_000)
  })

  it.each([
    ['leading zero', { seq: '01', next_offset: '5' }],
    ['numeric sequence', { seq: 1, next_offset: '5' }],
    ['uint64 overflow', { seq: '18446744073709551616', next_offset: '5' }],
    ['invalid origin', { seq: '0', next_offset: '5' }],
  ])('rejects a %s cursor', (_name, captured) => {
    const value = timelineWire()
    value.captured = captured
    expect(() => parseTimeline(value)).toThrow()
  })

  it('rejects unknown fields, states, ranges, and timestamps', () => {
    const extra = timelineWire()
    extra.extra = true
    expect(() => parseTimeline(extra)).toThrow(/unexpected field/)

    const state = timelineWire()
    state.spans = [
      {
        state: 'paused',
        start: { seq: '1', next_offset: '0' },
        start_at: '2026-10-04T12:00:00Z',
      },
    ]
    expect(() => parseTimeline(state)).toThrow(/state is unsupported/)

    const range = timelineWire()
    range.output = {
      range: { start: '0', end: '5' },
      retained: [{ start: '3', end: '2' }],
      missing: [],
    }
    expect(() => parseTimeline(range)).toThrow(/end precedes start/)

    const timestamp = timelineWire()
    timestamp.captured_at = '2026-02-30T12:00:00Z'
    expect(() => parseTimeline(timestamp)).toThrow(/invalid calendar date/)
  })

  it('keeps frame previews non-restorable and validates dimensions', () => {
    expect(parseFramePreview(frameWire())).toMatchObject({
      kind: 'frame_preview',
      cursor: { seq: 4n, nextOffset: 5n },
      rows: 40,
      columns: 120,
      restorable: false,
    })

    const restorable = frameWire()
    restorable.restorable = true
    expect(() => parseFramePreview(restorable)).toThrow(/non-restorable/)

    const oversized = frameWire()
    oversized.columns = 65_536
    expect(() => parseFramePreview(oversized)).toThrow(/1..65535/)
  })

  it('maps HTTP 410 to OutputExpiredError with bigint ranges', async () => {
    const fetchMock = vi.fn(async () =>
      new Response(
        JSON.stringify({
          error: 'recording: output expired',
          code: 'output_expired',
          session_id: 'agent-1',
          missing: [{ start: '0', end: '5' }],
        }),
        {
          status: 410,
          headers: { 'Content-Type': 'application/json' },
        },
      ),
    )
    vi.stubGlobal('fetch', fetchMock)

    const request = getAgentFrame('agent-1', {
      kind: 'sequence',
      seq: 9_007_199_254_740_993n,
    })

    await expect(request).rejects.toEqual(
      new OutputExpiredError('agent-1', [{ start: 0n, end: 5n }]),
    )
    expect(fetchMock).toHaveBeenCalledWith(
      '/api/v1/agents/agent-1/frame?seq=9007199254740993',
      {
        headers: { 'Content-Type': 'application/json' },
        signal: undefined,
      },
    )
  })

  it('posts exact decimal action context and opaque action ticket bodies', async () => {
    const responses = [
      Response.json({
        state_seq: '9007199254740993',
        actions: ['reply'],
        tickets: [{ action: 'reply', ticket: 'opaque-ticket' }],
        expires_at: '2026-10-08T18:10:00Z',
        screen: {
          captured_at: '2026-10-08T18:00:00Z',
          rows: ['Approve?'],
          truncated: false,
        },
      }),
      Response.json({
        state_seq: '9007199254740993',
        bytes_written: 12,
        action_seq: '9007199254740994',
        input_seq: '9007199254740995',
      }),
    ]
    const fetchMock = vi.fn(async () => {
      const response = responses.shift()
      if (response === undefined) throw new Error('unexpected request')
      return response
    })
    vi.stubGlobal('fetch', fetchMock)
    const signal = new AbortController().signal

    await expect(
      getAgentActionContext(
        'agent/one',
        formatUint64(9_007_199_254_740_993n),
        'device-1',
        signal,
      ),
    ).resolves.toMatchObject({
      stateSeq: '9007199254740993',
      actions: ['reply'],
    })
    await expect(
      executeAgentAction(
        'agent/one',
        'opaque-ticket',
        'ship it',
        signal,
      ),
    ).resolves.toMatchObject({
      actionSeq: '9007199254740994',
      inputSeq: '9007199254740995',
    })

    expect(fetchMock).toHaveBeenNthCalledWith(
      1,
      '/api/v1/agents/agent%2Fone/action-context',
      {
        headers: { 'Content-Type': 'application/json' },
        method: 'POST',
        body: JSON.stringify({
          blocked_seq: '9007199254740993',
          device_id: 'device-1',
        }),
        signal,
      },
    )
    expect(fetchMock).toHaveBeenNthCalledWith(
      2,
      '/api/v1/agents/agent%2Fone/actions',
      {
        headers: { 'Content-Type': 'application/json' },
        method: 'POST',
        body: JSON.stringify({
          ticket: 'opaque-ticket',
          reply: 'ship it',
        }),
        signal,
      },
    )
  })

  it('parses the complete session status contract', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () =>
        Response.json([
          {
            agent_id: 'agent-1',
            name: 'worker',
            vendor: 'generic',
            dir: '/workspace/drove',
            mode: 'interactive',
            state: 'blocked',
            state_seq: '42',
            created_at: '2026-10-04T12:00:00Z',
            updated_at: '2026-10-04T12:00:05Z',
            state_since: '2026-10-04T12:00:03Z',
            hook_policy: 'auto',
            hook_status: 'fallback',
            signal_injection: 'off',
            signal_injection_status: 'off',
            signal_injection_reason: 'unsupported',
            resumable: true,
            last_transition: {
              source: 'screen',
              event: 'blocked_prompt',
              confidence: 0.9,
              screen: {
                rule: 'approval',
                edge: 'present',
                region: 'footer',
                output_offset: 5,
                last_output_seq: 4,
                evidence: 'Approve?',
              },
            },
          },
        ]),
      ),
    )

    await expect(listAgents()).resolves.toMatchObject([
      {
        agent_id: 'agent-1',
        dir: '/workspace/drove',
        state_seq: '42',
        state_since: '2026-10-04T12:00:03Z',
        hook_policy: 'auto',
        signal_injection_reason: 'unsupported',
        resumable: true,
        last_transition: {
          source: 'screen',
          screen: { edge: 'present', output_offset: 5 },
        },
      },
    ])
  })

  it.each(['042', 42, '18446744073709551616'])(
    'rejects noncanonical state sequence %s',
    (stateSeq) => {
      expect(() =>
        parseAgentStatus({
          agent_id: 'agent-1',
          name: 'worker',
          vendor: 'generic',
          mode: 'interactive',
          state: 'blocked',
          state_seq: stateSeq,
          created_at: '2026-10-04T12:00:00Z',
          updated_at: '2026-10-04T12:00:05Z',
          state_since: '2026-10-04T12:00:03Z',
          hook_policy: 'auto',
          hook_status: 'fallback',
          signal_injection: 'off',
          signal_injection_status: 'off',
          signal_injection_reason: 'unsupported',
          resumable: false,
        }),
      ).toThrow(/state_seq must be a canonical uint64 decimal string/)
    },
  )

  it('rejects malformed expiry bodies instead of inventing missing ranges', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () =>
        new Response(
          JSON.stringify({
            error: 'gone',
            code: 'output_expired',
            session_id: 'agent-1',
            missing: [{ start: '5', end: '5' }],
          }),
          { status: 410, headers: { 'Content-Type': 'application/json' } },
        ),
      ),
    )

    await expect(
      getAgentFrame('agent-1', { kind: 'offset', offset: 5n }),
    ).rejects.toThrow(/non-empty missing ranges/)
  })
})
