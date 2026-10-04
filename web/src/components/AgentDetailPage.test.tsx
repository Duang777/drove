import { act, create } from 'react-test-renderer'
import type { ReactTestRenderer } from 'react-test-renderer'
import { describe, expect, it } from 'vitest'
import type { AgentStatus } from '../api/types'
import {
  createInitialAgentTerminalView,
  type TerminalControllerStore,
} from '../terminal/sessionController'
import { AgentDetailPage } from './AgentDetailPage'

describe('AgentDetailPage', () => {
  it('shows the persisted working directory in the compact header', async () => {
    const renderer = await renderDetail({
      ...agentFixture(),
      dir: '/workspace/drove',
    })

    const directory = renderer.root.findByProps({
      className: 'agent-detail-directory',
    })
    expect(directory.props.title).toBe('/workspace/drove')
    expect(directory.findByType('code').children).toEqual([
      '/workspace/drove',
    ])
  })

  it('labels historical sessions without directory metadata', async () => {
    const renderer = await renderDetail(agentFixture())

    const directory = renderer.root.findByProps({
      className: 'agent-detail-directory',
    })
    expect(directory.props.title).toBe('工作目录不可用')
    expect(directory.findByType('code').children).toEqual([
      '工作目录不可用',
    ])
  })
})

async function renderDetail(agent: AgentStatus): Promise<ReactTestRenderer> {
  const mounted: { current?: ReactTestRenderer } = {}
  await act(async () => {
    mounted.current = create(
      <AgentDetailPage
        agentID={agent.agent_id}
        agent={agent}
        onBack={() => {}}
        createController={() => new StaticController(agent.agent_id)}
      />,
    )
  })
  if (mounted.current === undefined) {
    throw new Error('expected agent detail page')
  }
  return mounted.current
}

class StaticController implements TerminalControllerStore {
  private readonly view

  constructor(agentID: string) {
    this.view = createInitialAgentTerminalView(agentID, 'read_write')
  }

  async start(): Promise<void> {}

  getSnapshot = () => this.view

  subscribe(): () => void {
    return () => {}
  }

  bindViewport(): void {}

  dispatch(): void {}

  dispose(): void {}
}

function agentFixture(): AgentStatus {
  return {
    agent_id: 'agent-1',
    name: 'worker',
    vendor: 'generic',
    mode: 'interactive',
    state: 'working',
    created_at: '2026-10-04T12:00:00Z',
    updated_at: '2026-10-04T12:00:05Z',
    hook_policy: 'off',
    hook_status: 'off',
    signal_injection: 'off',
    signal_injection_status: 'off',
    signal_injection_reason: 'configured_off',
  }
}
