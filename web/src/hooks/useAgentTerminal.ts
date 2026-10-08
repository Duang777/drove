import {
  useLayoutEffect,
  useMemo,
  useRef,
  useSyncExternalStore,
} from 'react'
import {
  TerminalSessionController,
  createInitialAgentTerminalView,
} from '../terminal/sessionController'
import type {
  AgentTerminalView,
  PlaybackAction,
  TerminalAccess,
  TerminalControllerStore,
  TerminalSessionControllerOptions,
} from '../terminal/sessionController'

export interface AgentTerminalHandle {
  readonly view: AgentTerminalView
  readonly bindViewport: (element: HTMLDivElement | null) => void
  readonly dispatch: (action: PlaybackAction) => void
}

export type TerminalControllerFactory = (
  options: TerminalSessionControllerOptions,
) => TerminalControllerStore

export interface UseAgentTerminalOptions {
  readonly createController?: TerminalControllerFactory
  readonly enabled?: boolean
}

interface ActiveController {
  readonly agentID: string
  readonly access: TerminalAccess
  readonly controller: TerminalControllerStore
  readonly unsubscribe: () => void
}

class AgentTerminalLifetime {
  private readonly listeners = new Set<() => void>()
  private active: ActiveController | null = null
  private viewport: HTMLDivElement | null = null
  private snapshot: AgentTerminalView

  constructor(agentID: string, access: TerminalAccess) {
    this.snapshot = createInitialAgentTerminalView(agentID, access)
  }

  getSnapshot = (): AgentTerminalView => this.snapshot

  subscribe = (listener: () => void): (() => void) => {
    this.listeners.add(listener)
    return () => {
      this.listeners.delete(listener)
    }
  }

  bindViewport = (element: HTMLDivElement | null): void => {
    this.viewport = element
    this.active?.controller.bindViewport(element)
  }

  dispatch = (action: PlaybackAction): void => {
    this.active?.controller.dispatch(action)
  }

  activate(
    agentID: string,
    access: TerminalAccess,
    factory: TerminalControllerFactory,
  ): void {
    const current = this.active
    if (current?.agentID === agentID && current.access === access) return
    this.deactivate()

    const controller = factory({ agentID, access })
    const update = () => {
      if (this.active?.controller !== controller) return
      this.snapshot = controller.getSnapshot()
      this.emit()
    }
    const unsubscribe = controller.subscribe(update)
    this.active = { agentID, access, controller, unsubscribe }
    this.snapshot = controller.getSnapshot()
    controller.bindViewport(this.viewport)
    this.emit()
    void controller.start()
  }

  deactivate(agentID?: string, access?: TerminalAccess): void {
    const current = this.active
    if (current === null) return
    if (
      agentID !== undefined &&
      (current.agentID !== agentID || current.access !== access)
    ) {
      return
    }
    this.active = null
    current.unsubscribe()
    current.controller.dispose()
  }

  private emit(): void {
    for (const listener of this.listeners) listener()
  }
}

const defaultControllerFactory: TerminalControllerFactory = (options) =>
  new TerminalSessionController(options)

export function useAgentTerminal(
  agentID: string,
  access: TerminalAccess,
  options: UseAgentTerminalOptions = {},
): AgentTerminalHandle {
  const lifetimeRef = useRef<AgentTerminalLifetime>()
  if (lifetimeRef.current === undefined) {
    lifetimeRef.current = new AgentTerminalLifetime(agentID, access)
  }
  const lifetime = lifetimeRef.current
  const factory = options.createController ?? defaultControllerFactory
  const enabled = options.enabled ?? true
  const storedView = useSyncExternalStore(
    lifetime.subscribe,
    lifetime.getSnapshot,
    lifetime.getSnapshot,
  )

  useLayoutEffect(() => {
    if (!enabled) {
      lifetime.deactivate(agentID, access)
      return
    }
    lifetime.activate(agentID, access, factory)
    return () => {
      lifetime.deactivate(agentID, access)
    }
  }, [access, agentID, enabled, factory, lifetime])

  const view =
    storedView.agentID === agentID && storedView.access === access
      ? storedView
      : createInitialAgentTerminalView(agentID, access)

  return useMemo(
    () => ({
      view,
      bindViewport: lifetime.bindViewport,
      dispatch: lifetime.dispatch,
    }),
    [lifetime.bindViewport, lifetime.dispatch, view],
  )
}
