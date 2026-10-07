import { describe, expect, it } from 'vitest'
import type { AgentState, AgentStatus, Event } from '../api/types'
import {
  formatBlockedDuration,
  projectFleetAgents,
} from './fleetProjection'

describe('fleet projection', () => {
  it('orders blocked agents by longest wait before active and terminal states', () => {
    const agents = [
      agentFixture('done', 'done', '2026-10-04T12:00:01Z'),
      agentFixture('blocked-new', 'blocked', '2026-10-04T12:04:00Z'),
      agentFixture('idle', 'idle', '2026-10-04T12:00:03Z'),
      agentFixture('blocked-old', 'blocked', '2026-10-04T12:02:00Z'),
      agentFixture('working', 'working', '2026-10-04T12:00:02Z'),
      agentFixture('stopped', 'stopped', '2026-10-04T12:00:04Z'),
    ]

    expect(projectFleetAgents(agents, []).map((agent) => agent.agentID)).toEqual([
      'blocked-old',
      'blocked-new',
      'working',
      'idle',
      'done',
      'stopped',
    ])
  })

  it('uses a newer state event for state, timing, evidence, and order', () => {
    const agents = [
      agentFixture('already-blocked', 'blocked', '2026-10-04T12:03:00Z'),
      agentFixture('newly-blocked', 'working', '2026-10-04T12:00:00Z'),
    ]
    const events: Event[] = [
      {
        seq: 17,
        timestamp: '2026-10-04T12:05:00Z',
        type: 'state_changed',
        agent_id: 'newly-blocked',
        from: 'working',
        to: 'blocked',
        payload: JSON.stringify({
          version: 3,
          source: 'screen',
          event: 'approval',
          confidence: 0.91,
          screen: {
            rule: 'approval',
            edge: 'present',
            region: 'footer',
            output_offset: 34,
            last_output_seq: 16,
            evidence: 'Approve?',
          },
        }),
      },
    ]

    const projected = projectFleetAgents(agents, events)

    expect(projected.map((agent) => agent.agentID)).toEqual([
      'already-blocked',
      'newly-blocked',
    ])
    expect(projected[1]).toMatchObject({
      state: 'blocked',
      stateSince: '2026-10-04T12:05:00Z',
      evidence: {
        source: 'screen',
        event: 'approval',
        screen: { rule: 'approval' },
      },
    })
  })

  it('formats a stable operator-facing blocked duration', () => {
    expect(formatBlockedDuration(0)).toBe('已等你 0 秒')
    expect(formatBlockedDuration(72_999)).toBe('已等你 1 分 12 秒')
    expect(formatBlockedDuration(3_672_000)).toBe(
      '已等你 1 小时 1 分 12 秒',
    )
  })
})

function agentFixture(
  agentID: string,
  state: AgentState,
  stateSince: string,
): AgentStatus {
  return {
    agent_id: agentID,
    name: agentID,
    vendor: 'generic',
    mode: 'interactive',
    state,
    created_at: '2026-10-04T12:00:00Z',
    updated_at: stateSince,
    state_since: stateSince,
    hook_policy: 'off',
    hook_status: 'off',
    signal_injection: 'off',
    signal_injection_status: 'off',
    signal_injection_reason: 'configured_off',
    resumable: false,
  }
}
