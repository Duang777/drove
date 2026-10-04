import { useEffect, useState, useCallback } from 'react'
import { listAgents } from './api/client'
import type { AgentStatus } from './api/types'
import { useAgentEvents } from './hooks/useAgentEvents'
import { AgentList } from './components/AgentList'
import { AgentDetailPage } from './components/AgentDetailPage'
import { EventLog } from './components/EventLog'
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
            agents={agents}
            events={events}
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
