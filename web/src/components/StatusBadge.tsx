import type { AgentState } from '../api/types'

export function StatusBadge({ state }: { state: AgentState }) {
  return <span className={`status-badge status-${state}`}>{state}</span>
}
