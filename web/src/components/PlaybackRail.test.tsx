import { act, create } from 'react-test-renderer'
import type { ReactTestRenderer } from 'react-test-renderer'
import { describe, expect, it } from 'vitest'
import type { TerminalTimeline } from '../api/types'
import {
  createInitialAgentTerminalView,
  type AgentTerminalView,
  type PlaybackAction,
} from '../terminal/sessionController'
import { PlaybackRail } from './PlaybackRail'

describe('PlaybackRail', () => {
  it('previews slider movement and commits only when the gesture ends', () => {
    const actions: PlaybackAction[] = []
    const mounted: { current?: ReactTestRenderer } = {}
    act(() => {
      mounted.current = create(
        <PlaybackRail
          view={viewWithTimeline()}
          dispatch={(action) => actions.push(action)}
        />,
      )
    })
    const renderer = mounted.current
    if (renderer === undefined) throw new Error('expected playback rail')

    act(() => {
      renderer.root.findByProps({ 'aria-label': '回放时间' }).props.onChange({
        currentTarget: { value: '2500' },
      })
    })
    expect(actions).toEqual([
      { kind: 'preview_time', atMillis: 3_500 },
    ])

    act(() => {
      renderer.root
        .findByProps({ 'aria-label': '回放时间' })
        .props.onPointerUp()
    })
    expect(actions).toEqual([
      { kind: 'preview_time', atMillis: 3_500 },
      { kind: 'commit_seek', atMillis: 3_500 },
    ])
  })

  it('dispatches playback and authoritative Blocked jumps', () => {
    const actions: PlaybackAction[] = []
    const renderer: { current?: ReactTestRenderer } = {}
    act(() => {
      renderer.current = create(
        <PlaybackRail
          view={{
            ...viewWithTimeline(),
            replay: {
              kind: 'ready',
              atMillis: 4_000,
              playback: 'paused',
              speed: 1,
            },
          }}
          dispatch={(action) => actions.push(action)}
        />,
      )
    })
    const mounted = renderer.current
    if (mounted === undefined) throw new Error('expected playback rail')

    act(() => {
      mounted.root
        .findByProps({ 'aria-label': '播放回放' })
        .props.onClick()
      mounted.root
        .findByProps({ className: 'blocked-jump' })
        .props.onClick()
    })

    expect(actions).toEqual([
      { kind: 'play' },
      { kind: 'jump_blocked', occurrence: 1 },
    ])
  })
})

function viewWithTimeline(): AgentTerminalView {
  return {
    ...createInitialAgentTerminalView('agent-1', 'read_write'),
    connection: 'live',
    timeline: timelineFixture(),
  }
}

function timelineFixture(): TerminalTimeline {
  return {
    sessionID: 'session-1',
    agentID: 'agent-1',
    captured: { seq: 12n, nextOffset: 24n },
    capturedAt: { iso: '1970-01-01T00:00:10.000Z', epochMillis: 10_000 },
    durationMillis: 9_000,
    output: {
      range: { start: 0n, end: 24n },
      retained: [{ start: 0n, end: 24n }],
      missing: [],
    },
    spans: [
      {
        state: 'working',
        start: { seq: 1n, nextOffset: 0n },
        end: { seq: 8n, nextOffset: 16n },
        startAt: {
          iso: '1970-01-01T00:00:01.000Z',
          epochMillis: 1_000,
        },
        endAt: {
          iso: '1970-01-01T00:00:07.000Z',
          epochMillis: 7_000,
        },
      },
      {
        state: 'blocked',
        start: { seq: 8n, nextOffset: 16n },
        startAt: {
          iso: '1970-01-01T00:00:07.000Z',
          epochMillis: 7_000,
        },
      },
    ],
    blocked: [
      {
        number: 1,
        span: {
          state: 'blocked',
          start: { seq: 8n, nextOffset: 16n },
          startAt: {
            iso: '1970-01-01T00:00:07.000Z',
            epochMillis: 7_000,
          },
        },
        jump: { seq: 8n, nextOffset: 16n },
        frameAvailable: true,
      },
    ],
  }
}
