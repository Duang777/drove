import { create } from 'react-test-renderer'
import { describe, expect, it } from 'vitest'
import type { Event } from '../api/types'
import { EventLog } from './EventLog'

describe('EventLog', () => {
  it('renders a redacted remote action summary without changing state', () => {
    const action: Event = {
      seq: 44,
      timestamp: '2026-10-08T18:00:00Z',
      type: 'agent.action',
      agent_id: 'agent-1',
      payload: JSON.stringify({
        version: 1,
        action: 'reply',
        channel: 'web_push',
        device_id: 'device-1',
        blocked_seq: '42',
        reply_bytes: 12,
        prompt_rule: 'codex.approval_prompt',
      }),
    }

    const renderer = create(<EventLog liveEvents={[action]} />)
    const row = renderer.root.findByProps({ className: 'event-row' })
    const text = row.children.flatMap((child) =>
      typeof child === 'string' ? [child] : child.children,
    )

    expect(row.findByProps({ className: 'event-action' })).toBeDefined()
    expect(text.join(' ')).toContain(
      'reply · web_push · 12 reply bytes · codex.approval_prompt',
    )
  })
})
