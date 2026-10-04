/**
 * 订阅 daemon 实时事件流的 React hook。
 * 组件卸载时自动关闭连接（幂等）。
 */
import { useEffect, useRef, useState } from 'react'
import type { AgentState, ConnectionState, Event } from '../api/types'
import { EventStream } from '../ws/eventStream'

const MAX_BUFFERED_EVENTS = 500

export function useAgentEvents(): {
  events: Event[]
  connection: ConnectionState
} {
  const [events, setEvents] = useState<Event[]>([])
  const [connection, setConnection] = useState<ConnectionState>('connecting')
  const streamRef = useRef<EventStream | null>(null)

  useEffect(() => {
    const stream = new EventStream({
      onEvent: (ev) => {
        setEvents((prev) => {
          const next = [...prev, ev]
          return next.length > MAX_BUFFERED_EVENTS
            ? next.slice(next.length - MAX_BUFFERED_EVENTS)
            : next
        })
      },
      onStateChange: setConnection,
    })
    streamRef.current = stream
    stream.connect()
    return () => {
      stream.close()
      streamRef.current = null
    }
  }, [])

  return { events, connection }
}

/** 事件流中某 agent 的实时聚合状态（本地投影，权威状态仍以 daemon 为准）。 */
export function latestAgentState(events: Event[], agentID: string): AgentState | null {
  for (let i = events.length - 1; i >= 0; i--) {
    const ev = events[i]
    if (ev.type === 'state_changed' && ev.agent_id === agentID && isAgentState(ev.to)) {
      return ev.to
    }
  }
  return null
}

function isAgentState(value: string | undefined): value is AgentState {
  switch (value) {
    case 'pending':
    case 'starting':
    case 'working':
    case 'blocked':
    case 'done':
    case 'idle':
    case 'stopped':
      return true
    default:
      return false
  }
}
