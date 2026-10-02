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
    <div className="min-h-screen bg-gray-50 p-6">
      <header className="mb-6 flex items-center justify-between">
        <div>
          <h1 className="text-xl font-bold text-gray-900">
            Drove <span className="font-normal text-gray-400">Agent 指挥台</span>
          </h1>
          <p className="text-sm text-gray-500">herdr 让 Agent 活着，Drove 让它们往对的方向跑。</p>
        </div>
        <span
          className={`rounded px-2 py-1 text-xs ${
            connection === 'open'
              ? 'bg-green-100 text-green-700'
              : connection === 'connecting'
                ? 'bg-amber-100 text-amber-700'
                : 'bg-red-100 text-red-700'
          }`}
        >
          {connection === 'open' ? '已连接' : connection === 'connecting' ? '连接中…' : '已断开（重连中）'}
        </span>
      </header>

      {loadErr && (
        <p className="mb-4 rounded border border-red-200 bg-red-50 p-2 text-sm text-red-600">
          daemon 不可达：{loadErr}（请先运行 <code className="font-mono">droved</code>，或确认
          <code className="font-mono">~/.drove/config.json</code> 的 api_bind）
        </p>
      )}

      <main className="grid grid-cols-1 gap-6 lg:grid-cols-2">
        <AgentList agents={agents} events={events} onChanged={refresh} />
        <EventLog liveEvents={events} />
      </main>
    </div>
  )
}
