import { useState } from 'react'
import type { TerminalSnapshot } from '../api/types'
import { startAgent, stopAgent } from '../api/client'
import type { FleetAgent } from '../fleet/fleetProjection'
import { AgentCard } from './AgentCard'

interface Props {
  agents: ReadonlyArray<FleetAgent>
  snapshots: ReadonlyMap<string, TerminalSnapshot>
  nowMillis: number
  onOpenAgent: (id: string) => void
  onChanged: () => void
}

export function AgentList({
  agents,
  snapshots,
  nowMillis,
  onOpenAgent,
  onChanged,
}: Props) {
  const [busyID, setBusyID] = useState<string | null>(null)
  const [vendor, setVendor] = useState('claude')
  const [command, setCommand] = useState('')
  const [err, setErr] = useState<string | null>(null)

  async function handleStart() {
    setErr(null)
    try {
      await startAgent({
        vendor,
        ...(vendor === 'generic' ? { command: command.trim() } : {}),
      })
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
          {vendor === 'generic' && (
            <input
              type="text"
              aria-label="Generic command"
              placeholder="/bin/cat"
              value={command}
              onChange={(event) => setCommand(event.target.value)}
            />
          )}
          <button
            type="button"
            onClick={handleStart}
            aria-label="启动 Agent"
            disabled={vendor === 'generic' && command.trim() === ''}
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
          {agents.map((agent, index) => (
            <AgentCard
              key={agent.agentID}
              fleetAgent={agent}
              snapshot={snapshots.get(agent.agentID)}
              shortcut={index < 9 ? index + 1 : undefined}
              nowMillis={nowMillis}
              busy={busyID === agent.agentID}
              onOpen={onOpenAgent}
              onStop={handleStop}
            />
          ))}
        </div>
      )}
    </section>
  )
}
