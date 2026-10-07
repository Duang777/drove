import { useCallback, useEffect, useMemo, useState } from 'react'
import { listAgents } from './api/client'
import type { AgentStatus } from './api/types'
import { useAgentEvents } from './hooks/useAgentEvents'
import { useFleetSnapshots } from './hooks/useFleetSnapshots'
import { AgentList } from './components/AgentList'
import { AgentDetailPage } from './components/AgentDetailPage'
import { EventLog } from './components/EventLog'
import { resolveFleetKeyboardAction } from './fleet/fleetKeyboard'
import { projectFleetAgents } from './fleet/fleetProjection'
import {
  appLocationHref,
  parseAppLocation,
  type AppLocation,
} from './navigation'

export default function App() {
  const { events, connection } = useAgentEvents()
  const [agents, setAgents] = useState<AgentStatus[]>([])
  const [loadErr, setLoadErr] = useState<string | null>(null)
  const [location, setLocation] = useState<AppLocation>(() =>
    parseAppLocation(window.location.search),
  )

  const refresh = useCallback(async () => {
    try {
      setAgents(await listAgents())
      setLoadErr(null)
    } catch (e) {
      setLoadErr(e instanceof Error ? e.message : String(e))
    }
  }, [])

  useEffect(() => {
    void refresh()
    const timer = setInterval(() => void refresh(), 5000)
    return () => clearInterval(timer)
  }, [refresh])

  useEffect(() => {
    const handlePopState = () => {
      setLocation(parseAppLocation(window.location.search))
    }
    window.addEventListener('popstate', handlePopState)
    return () => {
      window.removeEventListener('popstate', handlePopState)
    }
  }, [])

  const navigate = useCallback((next: AppLocation) => {
    window.history.pushState(null, '', appLocationHref(next))
    setLocation(next)
  }, [])

  const fleetAgents = useMemo(
    () => projectFleetAgents(agents, events),
    [agents, events],
  )
  const snapshotAgentIDs = useMemo(
    () =>
      fleetAgents
        .filter(
          (agent) =>
            agent.state !== 'done' && agent.state !== 'stopped',
        )
        .map((agent) => agent.agentID),
    [fleetAgents],
  )
  const snapshots = useFleetSnapshots(snapshotAgentIDs, {
    enabled: location.kind === 'fleet',
  })
  const nowMillis = useFleetClock(
    location.kind === 'fleet' &&
      fleetAgents.some((agent) => agent.state === 'blocked'),
  )

  useEffect(() => {
    const handleKeyDown = (event: KeyboardEvent) => {
      const action = resolveFleetKeyboardAction({
        location,
        key: event.key,
        hasModifier:
          event.metaKey || event.ctrlKey || event.altKey || event.shiftKey,
        editableTarget: isEditableTarget(event.target),
        orderedAgentIDs: fleetAgents.map((agent) => agent.agentID),
      })
      if (action === null) return
      event.preventDefault()
      switch (action.kind) {
        case 'open_agent':
          navigate({ kind: 'agent', agentID: action.agentID })
          return
        case 'back_to_fleet':
          navigate({ kind: 'fleet' })
          return
        default: {
          const exhaustive: never = action
          return exhaustive
        }
      }
    }
    window.addEventListener('keydown', handleKeyDown, true)
    return () => {
      window.removeEventListener('keydown', handleKeyDown, true)
    }
  }, [fleetAgents, location, navigate])

  const selectedAgent =
    location.kind === 'agent'
      ? agents.find((agent) => agent.agent_id === location.agentID)
      : undefined

  return (
    <div className="app-shell">
      <a className="skip-link" href="#main-content">
        跳到主内容
      </a>

      {location.kind === 'fleet' && (
        <header className="app-header">
          <div className="brand-block">
            <h1>
              Drove <span>Agent 指挥台</span>
            </h1>
            <p>herdr 让 Agent 活着，Drove 让它们往对的方向跑。</p>
          </div>
          <span className={`connection-status connection-${connection}`}>
            {connection === 'open'
              ? '已连接'
              : connection === 'connecting'
                ? '连接中…'
                : '已断开（重连中）'}
          </span>
        </header>
      )}

      {loadErr && (
        <p className="error-banner" role="alert">
          daemon 不可达：{loadErr}（请先运行 <code>droved</code>，或确认
          <code>~/.drove/config.json</code> 的 api_bind）
        </p>
      )}

      {location.kind === 'agent' ? (
        <AgentDetailPage
          agentID={location.agentID}
          agent={selectedAgent}
          onBack={() => navigate({ kind: 'fleet' })}
        />
      ) : (
        <main id="main-content" className="workspace">
          <AgentList
            agents={fleetAgents}
            snapshots={snapshots.byAgentID}
            nowMillis={nowMillis}
            onOpenAgent={(agentID) =>
              navigate({ kind: 'agent', agentID })
            }
            onChanged={refresh}
          />
          <EventLog liveEvents={events} />
        </main>
      )}
    </div>
  )
}

function useFleetClock(enabled: boolean): number {
  const [nowMillis, setNowMillis] = useState(() => Date.now())
  useEffect(() => {
    if (!enabled) return
    setNowMillis(Date.now())
    const timer = window.setInterval(() => {
      setNowMillis(Date.now())
    }, 250)
    return () => {
      window.clearInterval(timer)
    }
  }, [enabled])
  return nowMillis
}

function isEditableTarget(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false
  return (
    target.isContentEditable ||
    target.tagName === 'INPUT' ||
    target.tagName === 'TEXTAREA' ||
    target.tagName === 'SELECT'
  )
}
