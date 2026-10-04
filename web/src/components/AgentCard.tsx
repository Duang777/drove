import { SquareTerminal } from 'lucide-react'
import type { AgentStatus } from '../api/types'
import { appLocationHref } from '../navigation'
import { StatusBadge } from './StatusBadge'

interface Props {
  agent: AgentStatus
  liveState?: AgentStatus['state'] | null
  onOpen: (id: string) => void
  onStop: (id: string) => void
  busy: boolean
}

export function AgentCard({
  agent,
  liveState,
  onOpen,
  onStop,
  busy,
}: Props) {
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
        <div className="agent-card-controls">
          <a
            href={appLocationHref({
              kind: 'agent',
              agentID: agent.agent_id,
            })}
            className="button button-secondary button-with-icon"
            onClick={(event) => {
              if (
                event.button !== 0 ||
                event.metaKey ||
                event.ctrlKey ||
                event.shiftKey ||
                event.altKey
              ) {
                return
              }
              event.preventDefault()
              onOpen(agent.agent_id)
            }}
          >
            <SquareTerminal aria-hidden="true" size={15} />
            终端
          </a>
          <button
            type="button"
            onClick={() => onStop(agent.agent_id)}
            disabled={busy || state === 'stopped'}
            className="button button-danger"
          >
            停止
          </button>
        </div>
      </div>
      {agent.last_error && (
        <p className="agent-error" title={agent.last_error}>
          {agent.last_error}
        </p>
      )}
    </article>
  )
}
