import { CircleAlert, SquareTerminal } from 'lucide-react'
import type { TerminalSnapshot } from '../api/types'
import {
  formatBlockedDuration,
  type FleetAgent,
} from '../fleet/fleetProjection'
import { appLocationHref } from '../navigation'
import { StatusBadge } from './StatusBadge'

const PREVIEW_LINES = 6

interface Props {
  fleetAgent: FleetAgent
  snapshot?: TerminalSnapshot
  shortcut?: number
  nowMillis: number
  onOpen: (id: string) => void
  onStop: (id: string) => void
  busy: boolean
}

export function AgentCard({
  fleetAgent,
  snapshot,
  shortcut,
  nowMillis,
  onOpen,
  onStop,
  busy,
}: Props) {
  const { status: agent, state, stateSince, evidence } = fleetAgent
  const terminalState = state === 'done' || state === 'stopped'
  const preview = terminalPreview(snapshot, terminalState)
  const blockedSince = Date.parse(stateSince)
  const blockedDuration =
    state === 'blocked' && Number.isFinite(blockedSince)
      ? formatBlockedDuration(nowMillis - blockedSince)
      : null

  return (
    <article
      className={`agent-card ${state === 'blocked' ? 'agent-card-blocked' : ''}`}
    >
      <div className="agent-card-heading">
        <div className="agent-identity">
          <h3 title={agent.name}>{agent.name}</h3>
          <p>
            {agent.vendor} · pid {agent.pid ?? '—'} · {agent.agent_id.slice(0, 8)}
          </p>
        </div>
        <div className="agent-card-status">
          {shortcut !== undefined && (
            <kbd className="agent-shortcut">
              <span className="visually-hidden">快捷键 </span>
              {shortcut}
            </kbd>
          )}
          <StatusBadge state={state} />
        </div>
      </div>

      {blockedDuration !== null && (
        <p className="agent-blocked-wait" role="status">
          <CircleAlert size={14} aria-hidden="true" />
          {blockedDuration}
        </p>
      )}

      <a
        href={appLocationHref({
          kind: 'agent',
          agentID: agent.agent_id,
        })}
        className="agent-terminal-link"
        aria-label={`打开 ${agent.name} 的实时终端`}
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
        <span className="agent-terminal-bar">
          <SquareTerminal aria-hidden="true" size={13} />
          {snapshot === undefined
            ? terminalState
              ? '终端已结束'
              : '等待实时快照'
            : `${snapshot.columns} × ${snapshot.rows}`}
        </span>
        <pre>{preview}</pre>
      </a>

      <div className="agent-card-context">
        <span>{evidenceLabel(evidence)}</span>
        {snapshot !== undefined && (
          <time dateTime={snapshot.captured_at.iso}>
            {new Date(snapshot.captured_at.epochMillis).toLocaleTimeString()}
          </time>
        )}
      </div>

      <div className="agent-card-footer">
        <p>
          updated {new Date(agent.updated_at).toLocaleTimeString()}
        </p>
        <div className="agent-card-controls">
          <button
            type="button"
            onClick={() => onStop(agent.agent_id)}
            disabled={busy || terminalState}
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

function terminalPreview(
  snapshot: TerminalSnapshot | undefined,
  terminalState: boolean,
): string {
  if (snapshot === undefined) {
    return terminalState ? '会话已结束，无实时快照' : '正在等待终端快照'
  }
  if (snapshot.lines.length === 0) return '终端当前为空'
  return snapshot.lines.slice(-PREVIEW_LINES).join('\n')
}

function evidenceLabel(evidence: FleetAgent['evidence']): string {
  if (evidence === undefined) return 'evidence unavailable'
  const rule = evidence.source === 'screen'
    ? evidence.screen.rule
    : evidence.event
  return `${evidence.source} / ${rule}`
}
