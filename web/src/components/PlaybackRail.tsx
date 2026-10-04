import { Pause, Play, Radio, RefreshCw, X } from 'lucide-react'
import { useEffect, useState } from 'react'
import type { KeyboardEvent } from 'react'
import type {
  AgentTerminalView,
  PlaybackAction,
  PlaybackSpeed,
  TerminalReplayView,
} from '../terminal/sessionController'
import type {
  BlockedOccurrence,
  TerminalTimeline,
  TimelineStateSpan,
} from '../api/types'

const SEEK_KEYS = new Set([
  'ArrowLeft',
  'ArrowRight',
  'Home',
  'End',
  'PageUp',
  'PageDown',
])
const PLAYBACK_SPEEDS: ReadonlyArray<PlaybackSpeed> = [0.5, 1, 2, 4]

interface Props {
  readonly view: AgentTerminalView
  readonly dispatch: (action: PlaybackAction) => void
}

export function PlaybackRail({ view, dispatch }: Props) {
  const timeline = view.timeline
  const authoritativeAt = activeAtMillis(view)
  const [draftAtMillis, setDraftAtMillis] = useState(authoritativeAt)

  useEffect(() => {
    setDraftAtMillis(authoritativeAt)
  }, [authoritativeAt])

  if (timeline === null) {
    return (
      <section className="playback-rail is-loading" aria-label="回放控制">
        <p role="status">正在加载回放控制</p>
      </section>
    )
  }

  const origin = timeline.capturedAt.epochMillis - timeline.durationMillis
  const duration = Math.max(1, timeline.durationMillis)
  const offset = clamp(draftAtMillis - origin, 0, timeline.durationMillis)
  const ready = view.replay.kind === 'ready'
  const playing = ready && view.replay.playback === 'playing'
  const speed = ready ? view.replay.speed : 1

  const preview = (nextOffset: number) => {
    const atMillis = origin + clamp(nextOffset, 0, timeline.durationMillis)
    setDraftAtMillis(atMillis)
    dispatch({ kind: 'preview_time', atMillis })
  }
  const commit = () => {
    dispatch({ kind: 'commit_seek', atMillis: draftAtMillis })
  }

  return (
    <section className="playback-rail" aria-label="回放控制">
      <div className="playback-controls">
        <button
          type="button"
          className="icon-button"
          aria-label={playing ? '暂停回放' : '播放回放'}
          title={playing ? '暂停回放' : '播放回放'}
          disabled={!ready}
          onClick={() => dispatch({ kind: playing ? 'pause' : 'play' })}
        >
          {playing ? (
            <Pause size={17} aria-hidden="true" />
          ) : (
            <Play size={17} aria-hidden="true" />
          )}
        </button>

        <label className="speed-control">
          <span>速度</span>
          <select
            aria-label="回放速度"
            value={speed}
            disabled={!ready}
            onChange={(event) => {
              const next = Number(event.currentTarget.value)
              if (isPlaybackSpeed(next)) {
                dispatch({ kind: 'set_speed', speed: next })
              }
            }}
          >
            {PLAYBACK_SPEEDS.map((option) => (
              <option key={option} value={option}>
                {option}×
              </option>
            ))}
          </select>
        </label>

        <span className="playback-time" aria-live="polite">
          {formatDuration(offset)} / {formatDuration(timeline.durationMillis)}
        </span>

        <div className="playback-actions">
          {view.replay.kind === 'building' && (
            <button
              type="button"
              className="icon-button"
              aria-label="取消定位"
              title="取消定位"
              onClick={() => dispatch({ kind: 'cancel_seek' })}
            >
              <X size={17} aria-hidden="true" />
            </button>
          )}
          <button
            type="button"
            className="icon-button"
            aria-label="重试连接"
            title="重试连接"
            onClick={() => dispatch({ kind: 'retry' })}
          >
            <RefreshCw size={17} aria-hidden="true" />
          </button>
          <button
            type="button"
            className="button button-primary button-with-icon"
            disabled={view.replay.kind === 'live'}
            onClick={() => dispatch({ kind: 'go_live' })}
          >
            <Radio size={16} aria-hidden="true" />
            实时
          </button>
        </div>
      </div>

      <div className="timeline">
        <div className="timeline-track" aria-hidden="true">
          {timeline.spans.map((span, index) => (
            <TimelineSpan
              key={`${span.state}-${span.start.seq}-${index}`}
              span={span}
              timeline={timeline}
              origin={origin}
            />
          ))}
          {timeline.blocked.map((blocked) => (
            <BlockedMarker
              key={blocked.number}
              blocked={blocked}
              timeline={timeline}
              origin={origin}
            />
          ))}
        </div>
        <input
          className="timeline-slider"
          type="range"
          min={0}
          max={duration}
          step={1}
          value={offset}
          aria-label="回放时间"
          onChange={(event) => preview(Number(event.currentTarget.value))}
          onPointerUp={commit}
          onKeyUp={(event) => {
            if (isSeekKey(event)) commit()
          }}
        />
      </div>

      {timeline.blocked.length > 0 && (
        <div className="blocked-jumps" aria-label="阻塞时间点">
          {timeline.blocked.map((blocked) => (
            <button
              key={blocked.number}
              type="button"
              className="blocked-jump"
              onClick={() =>
                dispatch({
                  kind: 'jump_blocked',
                  occurrence: blocked.number,
                })
              }
            >
              阻塞 {blocked.number}
              <time>{formatClock(blocked.span.startAt.epochMillis)}</time>
            </button>
          ))}
        </div>
      )}
    </section>
  )
}

function TimelineSpan({
  span,
  timeline,
  origin,
}: {
  readonly span: TimelineStateSpan
  readonly timeline: TerminalTimeline
  readonly origin: number
}) {
  const start = percent(
    span.startAt.epochMillis - origin,
    timeline.durationMillis,
  )
  const end = percent(
    (span.endAt?.epochMillis ?? timeline.capturedAt.epochMillis) - origin,
    timeline.durationMillis,
  )
  return (
    <span
      className={`timeline-span timeline-span-${span.state}`}
      style={{ left: `${start}%`, width: `${Math.max(0.6, end - start)}%` }}
      title={`${span.state} · ${formatClock(span.startAt.epochMillis)}`}
    />
  )
}

function BlockedMarker({
  blocked,
  timeline,
  origin,
}: {
  readonly blocked: BlockedOccurrence
  readonly timeline: TerminalTimeline
  readonly origin: number
}) {
  const at = percent(
    blocked.span.startAt.epochMillis - origin,
    timeline.durationMillis,
  )
  return (
    <span
      className="timeline-blocked-marker"
      style={{ left: `${at}%` }}
      title={`阻塞 ${blocked.number}`}
    />
  )
}

function activeAtMillis(view: AgentTerminalView): number {
  if (view.previewAtMillis !== null) return view.previewAtMillis
  if (view.timeline === null) return 0
  return replayAtMillis(view.replay) ?? view.timeline.capturedAt.epochMillis
}

function replayAtMillis(replay: TerminalReplayView): number | null {
  switch (replay.kind) {
    case 'live':
      return null
    case 'building':
    case 'ready':
    case 'not_ready':
    case 'expired':
    case 'local_limit':
    case 'error':
      return replay.atMillis
    default: {
      const exhaustive: never = replay
      return exhaustive
    }
  }
}

function isPlaybackSpeed(value: number): value is PlaybackSpeed {
  return value === 0.5 || value === 1 || value === 2 || value === 4
}

function isSeekKey(event: KeyboardEvent<HTMLInputElement>): boolean {
  return SEEK_KEYS.has(event.key)
}

function percent(offset: number, duration: number): number {
  if (duration <= 0) return 0
  return clamp((offset / duration) * 100, 0, 100)
}

function clamp(value: number, minimum: number, maximum: number): number {
  return Math.min(maximum, Math.max(minimum, value))
}

function formatDuration(milliseconds: number): string {
  const totalSeconds = Math.max(0, Math.round(milliseconds / 1000))
  const minutes = Math.floor(totalSeconds / 60)
  const seconds = totalSeconds % 60
  return `${minutes}:${seconds.toString().padStart(2, '0')}`
}

function formatClock(milliseconds: number): string {
  return new Date(milliseconds).toLocaleTimeString('zh-CN', {
    hour12: false,
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  })
}
