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
    <section className="agent-section" aria-labelledby="agents-title">
      <div className="section-toolbar">
        <h2 id="agents-title">会话</h2>
        <div className="agent-actions">
        <select
          aria-label="Agent vendor"
          value={vendor}
          onChange={(e) => setVendor(e.target.value)}
        >
          <option value="claude">claude</option>
          <option value="codex">codex</option>
          <option value="generic">generic（自定义命令）</option>
        </select>
        <button
          type="button"
          onClick={handleStart}
          className="button button-primary"
        >
          启动 Agent
        </button>
        </div>
      </div>
      {err && <p className="inline-error" role="alert">{err}</p>}

      {agents.length === 0 ? (
        <p className="empty-state">暂无 Agent</p>
      ) : (
        <div className="agent-grid">
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
