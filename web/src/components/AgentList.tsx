import { useState } from 'react'
import type { AgentStatus } from '../api/types'
import { startAgent, stopAgent } from '../api/client'
import { AgentCard } from './AgentCard'
import { latestAgentState } from '../hooks/useAgentEvents'
import type { Event } from '../api/types'

interface Props {
  agents: AgentStatus[]
  events: Event[]
  onChanged: () => void
}

export function AgentList({ agents, events, onChanged }: Props) {
  const [busyID, setBusyID] = useState<string | null>(null)
  const [vendor, setVendor] = useState('claude')
  const [err, setErr] = useState<string | null>(null)

  const liveState = (id: string) => latestAgentState(events, id)

  async function handleStart() {
    setErr(null)
    try {
      await startAgent({ vendor })
      onChanged()
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e))
    }
  }

  async function handleStop(id: string) {
    setBusyID(id)
    setErr(null)
    try {
      await stopAgent(id)
      onChanged()
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e))
    } finally {
      setBusyID(null)
    }
  }

  return (
    <section className="space-y-3">
      <div className="flex items-center gap-2">
        <select
          value={vendor}
          onChange={(e) => setVendor(e.target.value)}
          className="rounded border border-gray-300 px-2 py-1 text-sm"
        >
          <option value="claude">claude</option>
          <option value="codex">codex</option>
          <option value="generic">generic（自定义命令）</option>
        </select>
        <button
          type="button"
          onClick={handleStart}
          className="rounded bg-blue-600 px-3 py-1 text-sm text-white hover:bg-blue-700"
        >
          启动 Agent
        </button>
        {err && <p className="text-xs text-red-500">{err}</p>}
      </div>

      {agents.length === 0 ? (
        <p className="py-8 text-center text-sm text-gray-400">暂无 Agent，点击上方按钮启动一个。</p>
      ) : (
        <div className="grid grid-cols-1 gap-3 md:grid-cols-2 lg:grid-cols-3">
          {agents.map((a) => (
            <AgentCard
              key={a.agent_id}
              agent={a}
              liveState={liveState(a.agent_id)}
              busy={busyID === a.agent_id}
              onStop={handleStop}
            />
          ))}
        </div>
      )}
    </section>
  )
}
