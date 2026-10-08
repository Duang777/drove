import { act, create } from 'react-test-renderer'
import type { ReactTestRenderer } from 'react-test-renderer'
import { describe, expect, it, vi } from 'vitest'
import { formatUint64 } from '../api/parsing'
import type { AgentStatus, TerminalSnapshot } from '../api/types'
import type { FleetAgent } from '../fleet/fleetProjection'
import { parseTerminalTextMessage } from '../ws/terminalStream'
import { AgentCard } from './AgentCard'

describe('AgentCard', () => {
  it('shows a bounded terminal tail and persistent blocked evidence', async () => {
    const onOpen = vi.fn()
    const renderer = await renderCard({
      fleetAgent: {
        agentID: 'agent-1',
        status: agentFixture(),
        state: 'blocked',
        stateSince: '2026-10-04T12:00:00Z',
        evidence: {
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
        },
      },
      snapshot: snapshotFixture([
        'old line',
        'step 1',
        'step 2',
        'step 3',
        'step 4',
        'step 5',
        'Approve?',
      ]),
      shortcut: 1,
      nowMillis: Date.parse('2026-10-04T12:07:12Z'),
      onOpen,
    })

    const card = renderer.root.findByType('article')
    expect(card.props.className).toContain('agent-card-blocked')
    expect(renderer.root.findByType('pre').children).toEqual([
      'step 1\nstep 2\nstep 3\nstep 4\nstep 5\nApprove?',
    ])
    expect(textContent(renderer)).toContain('screen / approval')
    expect(textContent(renderer)).toContain('已等你 7 分 12 秒')
    expect(
      renderer.root.findByProps({
        className: 'visually-hidden',
      }).children,
    ).toEqual(['快捷键 '])
    expect(
      renderer.root.findByProps({
        className: 'agent-shortcut',
      }).children.at(-1),
    ).toBe('1')

    act(() => {
      renderer.root.findByProps({
        'aria-label': '打开 worker 的实时终端',
      }).props.onClick({
        button: 0,
        metaKey: false,
        ctrlKey: false,
        shiftKey: false,
        altKey: false,
        preventDefault: vi.fn(),
      })
    })
    expect(onOpen).toHaveBeenCalledWith('agent-1')
  })
})

async function renderCard(props: {
  readonly fleetAgent: FleetAgent
  readonly snapshot: TerminalSnapshot
  readonly shortcut: number
  readonly nowMillis: number
  readonly onOpen: (agentID: string) => void
}): Promise<ReactTestRenderer> {
  let renderer: ReactTestRenderer | undefined
  await act(async () => {
    renderer = create(
      <AgentCard
        {...props}
        busy={false}
        onStop={() => {}}
      />,
    )
  })
  if (renderer === undefined) throw new Error('expected agent card')
  return renderer
}

function textContent(renderer: ReactTestRenderer): string {
  return renderer.root
    .findAll(() => true)
    .flatMap((node) => node.children)
    .filter((child): child is string => typeof child === 'string')
    .join(' ')
}

function snapshotFixture(lines: readonly string[]): TerminalSnapshot {
  const parsed = parseTerminalTextMessage(
    JSON.stringify({
      version: 2,
      type: 'snapshot',
      agent_id: 'agent-1',
      cursor: { seq: '2', next_offset: '3' },
      rows: 40,
      columns: 120,
      lines,
      truncated: false,
      restorable: false,
      captured_at: '2026-10-04T12:07:12Z',
    }),
  )
  if (parsed.kind !== 'stream' || parsed.message.kind !== 'snapshot') {
    throw new Error('expected parsed snapshot')
  }
  return parsed.message
}

function agentFixture(): AgentStatus {
  return {
    agent_id: 'agent-1',
    name: 'worker',
    vendor: 'generic',
    mode: 'interactive',
    state: 'blocked',
    state_seq: formatUint64(4n),
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
