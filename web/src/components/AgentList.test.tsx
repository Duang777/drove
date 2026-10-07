import { act, create } from 'react-test-renderer'
import type { ReactTestRenderer } from 'react-test-renderer'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { startAgent } from '../api/client'
import type { AgentStatus } from '../api/types'
import { AgentList } from './AgentList'

vi.mock('../api/client', () => ({
  startAgent: vi.fn(),
  stopAgent: vi.fn(),
}))

beforeEach(() => {
  vi.mocked(startAgent).mockReset()
  vi.mocked(startAgent).mockResolvedValue(agentFixture())
})

describe('AgentList', () => {
  it('requires and submits an explicit command for a generic agent', async () => {
    let renderer: ReactTestRenderer | undefined
    await act(async () => {
      renderer = create(
        <AgentList
          agents={[]}
          snapshots={new Map()}
          nowMillis={Date.parse('2026-10-04T12:00:00Z')}
          onOpenAgent={() => {}}
          onChanged={() => {}}
        />,
      )
    })
    if (renderer === undefined) throw new Error('expected agent list')
    const mounted = renderer

    act(() => {
      mounted.root.findByProps({
        'aria-label': 'Agent vendor',
      }).props.onChange({ target: { value: 'generic' } })
    })
    const command = mounted.root.findByProps({
      'aria-label': 'Generic command',
    })
    expect(
      mounted.root.findByProps({
        'aria-label': '启动 Agent',
      }).props.disabled,
    ).toBe(true)

    act(() => {
      command.props.onChange({ target: { value: '/bin/cat' } })
    })
    await act(async () => {
      await mounted.root.findByProps({
        'aria-label': '启动 Agent',
      }).props.onClick()
    })

    expect(startAgent).toHaveBeenCalledWith({
      vendor: 'generic',
      command: '/bin/cat',
    })
  })
})

function agentFixture(): AgentStatus {
  return {
    agent_id: 'agent-1',
    name: 'generic-agent',
    vendor: 'generic',
    mode: 'interactive',
    state: 'working',
    created_at: '2026-10-04T12:00:00Z',
    updated_at: '2026-10-04T12:00:00Z',
    state_since: '2026-10-04T12:00:00Z',
    hook_policy: 'off',
    hook_status: 'off',
    signal_injection: 'off',
    signal_injection_status: 'off',
    signal_injection_reason: 'configured_off',
    resumable: false,
  }
}
