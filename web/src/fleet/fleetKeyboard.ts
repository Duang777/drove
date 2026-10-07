import type { AppLocation } from '../navigation'

export type FleetKeyboardAction =
  | { readonly kind: 'open_agent'; readonly agentID: string }
  | { readonly kind: 'back_to_fleet' }

interface FleetKeyboardInput {
  readonly location: AppLocation
  readonly key: string
  readonly hasModifier: boolean
  readonly editableTarget: boolean
  readonly orderedAgentIDs: ReadonlyArray<string>
}

export function resolveFleetKeyboardAction(
  input: FleetKeyboardInput,
): FleetKeyboardAction | null {
  if (input.hasModifier) return null
  switch (input.location.kind) {
    case 'fleet': {
      if (input.editableTarget || !/^[1-9]$/.test(input.key)) return null
      const agentID = input.orderedAgentIDs[Number(input.key) - 1]
      return agentID === undefined
        ? null
        : { kind: 'open_agent', agentID }
    }
    case 'agent':
      return input.key === 'Escape' ? { kind: 'back_to_fleet' } : null
    default: {
      const exhaustive: never = input.location
      return exhaustive
    }
  }
}
