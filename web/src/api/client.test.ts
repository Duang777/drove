import { afterEach, describe, expect, it, vi } from 'vitest'
import {
  getAgentFrame,
  listAgents,
  OutputExpiredError,
  parseFramePreview,
  parseTimeline,
} from './client'
import { frameWire, timelineWire } from '../test/terminalFixtures'

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
            created_at: '2026-10-04T12:00:00Z',
            updated_at: '2026-10-04T12:00:05Z',
            hook_policy: 'auto',
            hook_status: 'fallback',
            signal_injection: 'off',
            signal_injection_status: 'off',
            signal_injection_reason: 'unsupported',
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
        hook_policy: 'auto',
        signal_injection_reason: 'unsupported',
        last_transition: {
          source: 'screen',
          screen: { edge: 'present', output_offset: 5 },
        },
      },
    ])
  })

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
