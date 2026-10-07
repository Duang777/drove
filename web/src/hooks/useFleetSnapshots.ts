import { useEffect, useMemo, useSyncExternalStore } from 'react'
import {
  FleetSnapshotController,
  type FleetSnapshotStreamFactory,
  type FleetSnapshotView,
} from '../fleet/fleetSnapshotController'

interface UseFleetSnapshotsOptions {
  readonly createStream?: FleetSnapshotStreamFactory
  readonly enabled?: boolean
}

export function useFleetSnapshots(
  agentIDs: ReadonlyArray<string>,
  options: UseFleetSnapshotsOptions = {},
): FleetSnapshotView {
  const createStream = options.createStream
  const enabled = options.enabled ?? true
  const controller = useMemo(
    () =>
      new FleetSnapshotController(
        createStream === undefined ? {} : { createStream },
      ),
    [createStream],
  )
  const view = useSyncExternalStore(
    controller.subscribe,
    controller.getSnapshot,
    controller.getSnapshot,
  )

  useEffect(() => {
    controller.setAgentIDs(agentIDs)
  }, [agentIDs, controller])

  useEffect(() => {
    if (!enabled) {
      controller.dispose()
      return
    }
    controller.start()
    return () => {
      controller.dispose()
    }
  }, [controller, enabled])

  return view
}
