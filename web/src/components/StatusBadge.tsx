import type { AgentState } from '../api/types'

const STYLE: Record<AgentState, string> = {
  pending: 'bg-gray-100 text-gray-600',
  starting: 'bg-blue-100 text-blue-700',
  working: 'bg-green-100 text-green-700',
  blocked: 'bg-amber-100 text-amber-700',
  done: 'bg-emerald-100 text-emerald-700',
  idle: 'bg-slate-100 text-slate-600',
  stopped: 'bg-red-100 text-red-700',
}

export function StatusBadge({ state }: { state: AgentState }) {
  return (
    <span
      className={`inline-block rounded px-2 py-0.5 text-xs font-medium ${STYLE[state] ?? STYLE.stopped}`}
    >
      {state}
    </span>
  )
}
