import { act, create } from 'react-test-renderer'
import type { ReactTestRenderer } from 'react-test-renderer'
import { describe, expect, it } from 'vitest'
import {
  createInitialAgentTerminalView,
  type AgentTerminalView,
} from '../terminal/sessionController'
import { TerminalViewport } from './TerminalViewport'

describe('TerminalViewport', () => {
  it('keeps reconnect feedback on the live terminal', () => {
    const renderer = renderViewport({
      ...baseView(),
      connection: 'reconnecting',
      connectionError: 'socket closed',
    })

    expect(
      renderer.root.findByProps({
        className: 'terminal-notice terminal-notice-status',
      }).children,
    ).toEqual(['连接中断，正在重连：socket closed'])
    expect(
      renderer.root.findByProps({
        className: 'terminal-viewport',
      }).props['data-terminal-mode'],
    ).toBe('live')
  })

  it('labels expired replay and renders frame previews outside xterm', () => {
    const renderer = renderViewport({
      ...baseView(),
      replay: {
        kind: 'expired',
        atMillis: 5_000,
        missing: [{ start: 2n, end: 8n }],
      },
      framePreview: {
        kind: 'frame_preview',
        sessionID: 'session-1',
        cursor: { seq: 4n, nextOffset: 8n },
        rows: 2,
        columns: 12,
        lines: ['build ready', '$ '],
        truncated: false,
        fidelity: 'exact_origin_replay',
        restorable: false,
      },
    })

    expect(
      renderer.root.findByProps({
        className: 'terminal-notice terminal-notice-error',
      }).children,
    ).toEqual(['该时间段的终端输出已过期'])
    expect(
      renderer.root.findByProps({
        className: 'terminal-viewport',
      }).props['data-terminal-mode'],
    ).toBe('replay')
    expect(
      renderer.root.findByProps({ 'aria-label': '只读帧预览' }).children,
    ).toEqual(['build ready\n$ '])
  })
})

function renderViewport(view: AgentTerminalView): ReactTestRenderer {
  const renderer: { current?: ReactTestRenderer } = {}
  act(() => {
    renderer.current = create(
      <TerminalViewport view={view} bindViewport={() => {}} />,
    )
  })
  if (renderer.current === undefined) {
    throw new Error('expected terminal viewport')
  }
  return renderer.current
}

function baseView(): AgentTerminalView {
  return {
    ...createInitialAgentTerminalView('agent-1', 'read_write'),
    connection: 'live',
  }
}
