import { useEffect, useState, useCallback } from 'react'
import { listAgents } from './api/client'
import type { AgentStatus } from './api/types'
import { useAgentEvents } from './hooks/useAgentEvents'
import { AgentList } from './components/AgentList'
import { EventLog } from './components/EventLog'

export default function App() {
  const { events, connection } = useAgentEvents()
  const [agents, setAgents] = useState<AgentStatus[]>([])
  const [loadErr, setLoadErr] = useState<string | null>(null)

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

  return (
    <div className="app-shell">
      <header className="app-header">
        <div className="brand-block">
          <h1>
            Drove <span>Agent 指挥台</span>
          </h1>
          <p>herdr 让 Agent 活着，Drove 让它们往对的方向跑。</p>
        </div>
        <span className={`connection-status connection-${connection}`}>
          {connection === 'open' ? '已连接' : connection === 'connecting' ? '连接中…' : '已断开（重连中）'}
        </span>
      </header>

      {loadErr && (
        <p className="error-banner" role="alert">
          daemon 不可达：{loadErr}（请先运行 <code>droved</code>，或确认
          <code>~/.drove/config.json</code> 的 api_bind）
        </p>
      )}

      <main className="workspace">
        <AgentList agents={agents} events={events} onChanged={refresh} />
        <EventLog liveEvents={events} />
      </main>
    </div>
  )
}
