import { act, create } from 'react-test-renderer'
import type { ReactTestRenderer } from 'react-test-renderer'
import { describe, expect, it } from 'vitest'
import type {
  AgentTerminalHandle,
  TerminalControllerFactory,
} from './useAgentTerminal'
import { useAgentTerminal } from './useAgentTerminal'
import {
  createInitialAgentTerminalView,
  type AgentTerminalView,
  type PlaybackAction,
  type TerminalAccess,
  type TerminalControllerStore,
  type TerminalSessionControllerOptions,
} from '../terminal/sessionController'

describe('useAgentTerminal', () => {
  it('disposes the old controller before activating a new agent or access mode', async () => {
    const events: string[] = []
    const controllers: FakeController[] = []
    let handle: AgentTerminalHandle | undefined
    const factory: TerminalControllerFactory = (options) => {
      events.push(`create:${options.agentID}:${options.access}`)
      const controller = new FakeController(options, events)
      controllers.push(controller)
      return controller
    }

    function Probe(props: {
      readonly agentID: string
      readonly access: TerminalAccess
    }): null {
      handle = useAgentTerminal(props.agentID, props.access, {
        createController: factory,
      })
      return null
    }

    const renderer: { current?: ReactTestRenderer } = {}
    await act(async () => {
      renderer.current = create(
        <Probe agentID="agent-1" access="read_write" />,
      )
    })
    expect(controllers).toHaveLength(1)
    expect(controllers[0]?.startCalls).toBe(1)
    expect(Object.keys(requireHandle(handle)).sort()).toEqual([
      'bindViewport',
      'dispatch',
      'view',
    ])

    const mounted = renderer.current
    if (mounted === undefined) throw new Error('expected mounted hook probe')
    await act(async () => {
      mounted.update(
        <Probe agentID="agent-2" access="read_only" />,
      )
    })

    expect(controllers).toHaveLength(2)
    expect(events).toEqual([
      'create:agent-1:read_write',
      'start:agent-1:read_write',
      'dispose:agent-1:read_write',
      'create:agent-2:read_only',
      'start:agent-2:read_only',
    ])
    expect(requireHandle(handle).view).toMatchObject({
      agentID: 'agent-2',
      access: 'read_only',
    })

    act(() => {
      requireHandle(handle).dispatch({ kind: 'retry' })
      controllers[1]?.setConnection('live')
    })
    expect(controllers[1]?.actions).toEqual([{ kind: 'retry' }])
    expect(requireHandle(handle).view.connection).toBe('live')

    act(() => {
      mounted.unmount()
    })
    expect(controllers[0]?.disposeCalls).toBe(1)
    expect(controllers[1]?.disposeCalls).toBe(1)
  })

  it('does not attach or resize a terminal while remote approval is pending', async () => {
    const events: string[] = []
    const controllers: FakeController[] = []
    const factory: TerminalControllerFactory = (options) => {
      const controller = new FakeController(options, events)
      controllers.push(controller)
      return controller
    }

    function Probe({ enabled }: { readonly enabled: boolean }): null {
      useAgentTerminal('agent-1', 'read_write', {
        createController: factory,
        enabled,
      })
      return null
    }

    let renderer: ReactTestRenderer | undefined
    await act(async () => {
      renderer = create(<Probe enabled={false} />)
    })
    expect(controllers).toHaveLength(0)

    await act(async () => {
      renderer?.update(<Probe enabled />)
    })
    expect(controllers).toHaveLength(1)
    expect(controllers[0]?.startCalls).toBe(1)
  })
})

class FakeController implements TerminalControllerStore {
  readonly actions: PlaybackAction[] = []
  startCalls = 0
  disposeCalls = 0
  private snapshot: AgentTerminalView
  private readonly listeners = new Set<() => void>()
  private readonly options: TerminalSessionControllerOptions
  private readonly events: string[]

  constructor(
    options: TerminalSessionControllerOptions,
    events: string[],
  ) {
    this.options = options
    this.events = events
    this.snapshot = createInitialAgentTerminalView(
      options.agentID,
      options.access,
    )
  }

  async start(): Promise<void> {
    this.startCalls += 1
    this.events.push(
      `start:${this.options.agentID}:${this.options.access}`,
    )
  }

  getSnapshot = (): AgentTerminalView => this.snapshot

  subscribe = (listener: () => void): (() => void) => {
    this.listeners.add(listener)
    return () => {
      this.listeners.delete(listener)
    }
  }

  bindViewport(): void {}

  dispatch = (action: PlaybackAction): void => {
    this.actions.push(action)
  }

  dispose(): void {
    this.disposeCalls += 1
    this.events.push(
      `dispose:${this.options.agentID}:${this.options.access}`,
    )
  }

  setConnection(connection: AgentTerminalView['connection']): void {
    this.snapshot = { ...this.snapshot, connection }
    for (const listener of this.listeners) listener()
  }
}

function requireHandle(
  handle: AgentTerminalHandle | undefined,
): AgentTerminalHandle {
  if (handle === undefined) throw new Error('expected terminal handle')
  return handle
}
