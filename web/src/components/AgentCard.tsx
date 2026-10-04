import type { AgentStatus } from '../api/types'
import { StatusBadge } from './StatusBadge'

interface Props {
  agent: AgentStatus
  liveState?: AgentStatus['state'] | null
  onStop: (id: string) => void
  busy: boolean
}

export function AgentCard({ agent, liveState, onStop, busy }: Props) {
  const state = liveState ?? agent.state

  return (
    <article className="agent-card">
      <div className="agent-card-heading">
        <div className="agent-identity">
          <h3 title={agent.name}>{agent.name}</h3>
          <p>
            {agent.vendor} · pid {agent.pid ?? '—'} · {agent.agent_id.slice(0, 8)}
          </p>
        </div>
        <StatusBadge state={state} />
      </div>
      <div className="agent-card-footer">
        <p>
          updated {new Date(agent.updated_at).toLocaleTimeString()}
        </p>
        <button
          type="button"
          onClick={() => onStop(agent.agent_id)}
          disabled={busy || state === 'stopped'}
          className="button button-danger"
        >
          停止
        </button>
      </div>
      {agent.last_error && (
        <p className="agent-error" title={agent.last_error}>
          {agent.last_error}
        </p>
      )}
    </article>
  )
}
