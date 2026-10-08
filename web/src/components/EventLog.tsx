import { useEffect, useState } from 'react'
import type { Event, EventRow } from '../api/types'
import { replayAgent } from '../api/client'

const KIND_STYLE: Record<string, string> = {
  state_changed: 'event-state',
  output: 'event-output',
  'output.chunk': 'event-chunk',
  error: 'event-error',
  session_lifecycle: 'event-lifecycle',
  'agent.input': 'event-input',
  'agent.action': 'event-action',
  'agent.signal': 'event-signal',
}

interface Props {
  /** 实时事件流（WebSocket）。 */
  liveEvents: Event[]
  /** 选中要回放的 agent；为空则展示实时流。 */
  replayID?: string
}

export function EventLog({ liveEvents, replayID }: Props) {
  const [rows, setRows] = useState<EventRow[] | null>(null)

  useEffect(() => {
    if (!replayID) {
      setRows(null)
      return
    }
    let cancelled = false
    replayAgent(replayID)
      .then((r) => {
        if (!cancelled) setRows(r)
      })
      .catch(() => {
        if (!cancelled) setRows([])
      })
    return () => {
      cancelled = true
    }
  }, [replayID])

  const replayRows = replayID === undefined ? null : rows

  return (
    <section className="event-section" aria-labelledby="events-title">
      <div className="section-toolbar">
        <h2 id="events-title">
          {replayID === undefined || replayRows === null
            ? '实时事件'
            : `回放 ${replayID.slice(0, 8)} · ${replayRows.length}`}
        </h2>
      </div>
      <div className="event-log">
        <div className="event-scroll">
        {replayRows !== null
          ? replayRows.map((r) => (
              <Row
                key={r.Seq}
                ts={r.Timestamp}
                kind={r.Type}
                text={formatEventText(r.Type, r.Payload || r.Reason)}
              />
            ))
          : liveEvents.map((ev) => (
              <Row
                key={ev.seq}
                ts={ev.timestamp}
                kind={ev.type}
                text={formatEventText(
                  ev.type,
                  ev.payload ??
                    (ev.to
                      ? `${ev.from ?? '?'} → ${ev.to}${ev.reason ? ' · ' + ev.reason : ''}`
                      : ev.reason ?? ''),
                )}
              />
            ))}
        {replayRows === null && liveEvents.length === 0 && (
          <p className="event-empty">等待事件</p>
        )}
        </div>
      </div>
    </section>
  )
}

function formatEventText(kind: string, text: string): string {
  if (kind === 'output.chunk') return formatOutputChunk(text)
  if (kind === 'agent.action') return formatAgentAction(text)
  if (kind !== 'agent.signal' || text === '') return text
  try {
    const value: unknown = JSON.parse(text)
    if (!isRecord(value)) return text
    const parts = [value.source, value.kind, value.vendor_event, value.outcome].filter(
      (part): part is string => typeof part === 'string' && part !== '',
    )
    return parts.length > 0 ? parts.join(' · ') : text
  } catch {
    return text
  }
}

function formatAgentAction(text: string): string {
  try {
    const value: unknown = JSON.parse(text)
    if (!isRecord(value)) return 'invalid action metadata'
    const action = value.action
    const channel = value.channel
    const replyBytes = value.reply_bytes
    const promptRule = value.prompt_rule
    if (
      typeof action !== 'string' ||
      typeof channel !== 'string' ||
      typeof replyBytes !== 'number' ||
      !Number.isSafeInteger(replyBytes) ||
      replyBytes < 0 ||
      typeof promptRule !== 'string'
    ) {
      return 'invalid action metadata'
    }
    return `${action} · ${channel} · ${replyBytes} reply bytes · ${promptRule}`
  } catch {
    return 'invalid action metadata'
  }
}

function formatOutputChunk(text: string): string {
  try {
    const value: unknown = JSON.parse(text)
    if (!isRecord(value)) return 'invalid output chunk metadata'
    const offset = value.offset
    const length = value.len
    if (
      typeof offset !== 'number' ||
      !Number.isSafeInteger(offset) ||
      offset < 0 ||
      typeof length !== 'number' ||
      !Number.isSafeInteger(length) ||
      length < 1
    ) {
      return 'invalid output chunk metadata'
    }
    return `offset ${offset} · ${length} bytes`
  } catch {
    return 'invalid output chunk metadata'
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function Row({ ts, kind, text }: { ts: string; kind: string; text: string }) {
  const time = new Date(ts).toLocaleTimeString('zh-CN', { hour12: false })
  return (
    <p className="event-row" title={text}>
      <time>{time}</time>{' '}
      <span className={KIND_STYLE[kind] ?? 'event-default'}>[{kind}]</span>{' '}
      <span className="event-text">{text}</span>
    </p>
  )
}
