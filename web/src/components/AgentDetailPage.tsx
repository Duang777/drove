import { ArrowLeft } from 'lucide-react'
import type { AgentStatus } from '../api/types'
import {
  useAgentTerminal,
  type TerminalControllerFactory,
} from '../hooks/useAgentTerminal'
import { appLocationHref } from '../navigation'
import type {
  TerminalConnectionState,
} from '../terminal/sessionController'
import { PlaybackRail } from './PlaybackRail'
import { StatusBadge } from './StatusBadge'
import { TerminalViewport } from './TerminalViewport'

interface Props {
  readonly agentID: string
  readonly agent?: AgentStatus
  readonly onBack: () => void
  readonly createController?: TerminalControllerFactory
}

export function AgentDetailPage({
  agentID,
  agent,
  onBack,
  createController,
}: Props) {
  const terminal = useAgentTerminal(
    agentID,
    'read_write',
    createController === undefined ? {} : { createController },
  )

  return (
    <main id="main-content" className="agent-detail">
      <header className="agent-detail-header">
        <a
          href={appLocationHref({ kind: 'fleet' })}
          className="icon-button detail-back"
          aria-label="返回会话列表"
          title="返回会话列表"
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
            onBack()
          }}
        >
          <ArrowLeft size={18} aria-hidden="true" />
        </a>
        <div className="agent-detail-identity">
          <h2>{agent?.name ?? agentID.slice(0, 12)}</h2>
          <p>
            {agent?.vendor ?? 'agent'} · <code>{agentID}</code>
          </p>
        </div>
        <div className="agent-detail-status">
          {agent !== undefined && <StatusBadge state={agent.state} />}
          <span
            className={`terminal-connection terminal-connection-${terminal.view.connection}`}
          >
            {connectionLabel(terminal.view.connection)}
          </span>
        </div>
      </header>

      <PlaybackRail view={terminal.view} dispatch={terminal.dispatch} />
      <TerminalViewport
        view={terminal.view}
        bindViewport={terminal.bindViewport}
      />
    </main>
  )
}

function connectionLabel(connection: TerminalConnectionState): string {
  switch (connection) {
    case 'loading':
      return '加载中'
    case 'connecting':
      return '连接中'
    case 'syncing':
      return '同步中'
    case 'live':
      return '已连接'
    case 'reconnecting':
      return '重连中'
    case 'error':
      return '连接失败'
    case 'disposed':
      return '已关闭'
    default: {
      const exhaustive: never = connection
      return exhaustive
    }
  }
}
