import type { AgentStatus } from '../api/types'
import { StatusBadge } from './StatusBadge'

interface Props {
  agent: AgentStatus
  liveState?: string | null
  onStop: (id: string) => void
  busy: boolean
}

export function AgentCard({ agent, liveState, onStop, busy }: Props) {
  const state = (liveState as AgentStatus['state']) ?? agent.state

  return (
    <div className="rounded border border-gray-200 bg-white p-4 shadow-sm">
      <div className="flex items-center justify-between">
        <div className="min-w-0">
          <p className="truncate text-sm font-semibold text-gray-900">{agent.name}</p>
          <p className="truncate text-xs text-gray-500">
            {agent.vendor} · pid {agent.pid ?? '—'} · {agent.agent_id.slice(0, 8)}
          </p>
        </div>
        <StatusBadge state={state} />
      </div>
      <div className="mt-3 flex items-center justify-between">
        <p className="text-xs text-gray-400">
          updated {new Date(agent.updated_at).toLocaleTimeString()}
        </p>
        <button
          type="button"
          onClick={() => onStop(agent.agent_id)}
          disabled={busy || state === 'stopped'}
          className="rounded border border-red-200 px-2 py-1 text-xs text-red-600 hover:bg-red-50 disabled:cursor-not-allowed disabled:opacity-40"
        >
          停止
        </button>
      </div>
      {agent.last_error && (
        <p className="mt-2 truncate text-xs text-red-500" title={agent.last_error}>
          {agent.last_error}
        </p>
      )}
    </div>
  )
}
