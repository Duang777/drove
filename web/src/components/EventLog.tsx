import { useEffect, useState } from 'react'
import type { Event, EventRow } from '../api/types'
import { replayAgent } from '../api/client'

const KIND_STYLE: Record<string, string> = {
  state_changed: 'text-purple-600',
  output: 'text-gray-700',
  error: 'text-red-600',
  session_lifecycle: 'text-blue-600',
  'agent.input': 'text-cyan-600',
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

  const isReplay = replayID !== undefined && rows !== null

  return (
    <div className="rounded border border-gray-200 bg-gray-950 p-3 font-mono text-xs">
      <p className="mb-2 text-gray-400">
        {isReplay ? `回放：${replayID.slice(0, 8)}（${rows?.length ?? 0} 条）` : '实时事件流'}
      </p>
      <div className="max-h-96 space-y-0.5 overflow-y-auto">
        {isReplay
          ? rows!.map((r) => (
              <Row key={r.Seq} ts={r.Timestamp} kind={r.Type} text={r.Payload || r.Reason} />
            ))
          : liveEvents.map((ev) => (
              <Row
                key={ev.seq}
                ts={ev.timestamp}
                kind={ev.type}
                text={ev.payload ?? (ev.to ? `${ev.from ?? '?'} → ${ev.to}${ev.reason ? ' · ' + ev.reason : ''}` : ev.reason ?? '')}
              />
            ))}
        {!isReplay && liveEvents.length === 0 && (
          <p className="text-gray-600">等待事件…（连接后启动一个 agent 即可看到）</p>
        )}
      </div>
    </div>
  )
}

function Row({ ts, kind, text }: { ts: string; kind: string; text: string }) {
  const time = new Date(ts).toLocaleTimeString('zh-CN', { hour12: false })
  return (
    <p className="truncate">
      <span className="text-gray-500">{time}</span>{' '}
      <span className={KIND_STYLE[kind] ?? 'text-gray-400'}>[{kind}]</span>{' '}
      <span className="text-gray-300">{text}</span>
    </p>
  )
}
