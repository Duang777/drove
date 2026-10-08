import { act, create } from 'react-test-renderer'
import type { ReactTestRenderer } from 'react-test-renderer'
import { describe, expect, it, vi } from 'vitest'
import { formatUint64 } from '../api/parsing'
import type { RemoteActionContext } from '../api/types'
import {
  RemoteActionPanel,
  validateActionReply,
} from './RemoteActionPanel'

describe('RemoteActionPanel', () => {
  it('loads fresh in-memory tickets and confirms approval before sending', async () => {
    const loadContext = vi.fn(async () => actionContext())
    const submitAction = vi.fn(async () => ({
      stateSeq: formatUint64(42n),
      bytesWritten: 1,
      actionSeq: formatUint64(43n),
      inputSeq: formatUint64(44n),
    }))
    const renderer = await renderPanel({ loadContext, submitAction })

    expect(loadContext).toHaveBeenCalledWith(
      'agent-1',
      formatUint64(42n),
      'device-1',
      expect.any(AbortSignal),
    )
    expect(renderer.root.findByType('pre').children).toEqual([
      'Review this command?\nPress enter to confirm',
    ])

    act(() => {
      renderer.root
        .findByProps({ 'aria-label': '批准请求' })
        .props.onClick()
    })
    expect(submitAction).not.toHaveBeenCalled()

    await act(async () => {
      renderer.root
        .findByProps({ 'aria-label': '确认批准请求' })
        .props.onClick()
    })
    expect(submitAction).toHaveBeenCalledWith(
      'agent-1',
      'approve-ticket',
      '',
      expect.any(AbortSignal),
    )
    expect(textContent(renderer)).toContain('已发送，等待 Agent 状态更新')
    expect(
      renderer.root.findAllByProps({ 'aria-label': '批准请求' }),
    ).toHaveLength(0)
  })

  it('validates a bounded reply and clears tickets after a failed submission', async () => {
    const loadContext = vi.fn(async () => actionContext())
    const submitAction = vi.fn(async () => {
      throw new Error('action is no longer available')
    })
    const renderer = await renderPanel({ loadContext, submitAction })
    const reply = renderer.root.findByProps({ 'aria-label': '回复 Agent' })

    act(() => {
      reply.props.onChange({ currentTarget: { value: 'first\nsecond' } })
    })
    act(() => {
      renderer.root
        .findByProps({ 'aria-label': '发送回复' })
        .props.onClick()
    })
    expect(submitAction).not.toHaveBeenCalled()
    expect(textContent(renderer)).toContain('回复不能包含控制字符')

    act(() => {
      reply.props.onChange({ currentTarget: { value: 'use read only' } })
    })
    await act(async () => {
      renderer.root
        .findByProps({ 'aria-label': '发送回复' })
        .props.onClick()
    })
    expect(submitAction).toHaveBeenCalledWith(
      'agent-1',
      'reply-ticket',
      'use read only',
      expect.any(AbortSignal),
    )
    expect(textContent(renderer)).toContain('action is no longer available')
    expect(
      renderer.root.findAllByProps({ 'aria-label': '发送回复' }),
    ).toHaveLength(0)
    expect(
      renderer.root.findByProps({ 'aria-label': '重新加载远程操作' }),
    ).toBeDefined()
  })

  it('does not request tickets without an active browser device', async () => {
    const loadContext = vi.fn(async () => actionContext())
    const renderer = await renderPanel({
      loadContext,
      submitAction: vi.fn(),
      resolveDeviceID: () => null,
    })

    expect(loadContext).not.toHaveBeenCalled()
    expect(textContent(renderer)).toContain('此设备尚未启用浏览器通知')
  })
})

describe('validateActionReply', () => {
  it('trims valid text and enforces the UTF-8 byte limit', () => {
    expect(validateActionReply('  ship it  ')).toEqual({
      kind: 'valid',
      reply: 'ship it',
    })
    expect(validateActionReply('')).toMatchObject({ kind: 'invalid' })
    expect(validateActionReply('x'.repeat(4_097))).toMatchObject({
      kind: 'invalid',
    })
    expect(validateActionReply('界'.repeat(1_366))).toMatchObject({
      kind: 'invalid',
    })
  })
})

async function renderPanel(overrides: {
  readonly loadContext: (
    ...args: Parameters<
      NonNullable<React.ComponentProps<typeof RemoteActionPanel>['loadContext']>
    >
  ) => Promise<RemoteActionContext>
  readonly submitAction: NonNullable<
    React.ComponentProps<typeof RemoteActionPanel>['submitAction']
  >
  readonly resolveDeviceID?: () => string | null
}): Promise<ReactTestRenderer> {
  let renderer: ReactTestRenderer | undefined
  await act(async () => {
    renderer = create(
      <RemoteActionPanel
        agentID="agent-1"
        blockedSeq={formatUint64(42n)}
        resolveDeviceID={overrides.resolveDeviceID ?? (() => 'device-1')}
        loadContext={overrides.loadContext}
        submitAction={overrides.submitAction}
      />,
    )
  })
  if (renderer === undefined) {
    throw new Error('expected remote action panel')
  }
  return renderer
}

function actionContext(): RemoteActionContext {
  return {
    stateSeq: formatUint64(42n),
    actions: ['approve', 'deny', 'reply'],
    tickets: [
      { action: 'approve', ticket: 'approve-ticket' },
      { action: 'deny', ticket: 'deny-ticket' },
      { action: 'reply', ticket: 'reply-ticket' },
    ],
    expiresAt: {
      iso: '2026-10-08T18:10:00Z',
      epochMillis: 1_791_483_000_000,
    },
    screen: {
      capturedAt: {
        iso: '2026-10-08T18:00:00Z',
        epochMillis: 1_791_482_400_000,
      },
      rows: ['Review this command?', 'Press enter to confirm'],
      truncated: false,
    },
  }
}

function textContent(renderer: ReactTestRenderer): string {
  return renderer.root
    .findAll(() => true)
    .flatMap((node) => node.children)
    .filter((child): child is string => typeof child === 'string')
    .join(' ')
}
