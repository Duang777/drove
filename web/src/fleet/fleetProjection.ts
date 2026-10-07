import type {
  AgentState,
  AgentStatus,
  Event,
  StateEvidence,
} from '../api/types'
import { requireRecord } from '../api/parsing'
import { parseAgentState, parseStateEvidence } from '../api/resourceParsing'

export interface FleetAgent {
  readonly agentID: string
  readonly status: AgentStatus
  readonly state: AgentState
  readonly stateSince: string
  readonly evidence?: StateEvidence
}

interface LiveState {
  readonly state: AgentState
  readonly stateSince: string
  readonly stateSinceMillis: number
  readonly evidence?: StateEvidence
}

const statePriority: Record<AgentState, number> = {
  blocked: 0,
  pending: 1,
  starting: 1,
  working: 1,
  idle: 2,
  done: 3,
  stopped: 3,
}

export function projectFleetAgents(
  agents: ReadonlyArray<AgentStatus>,
  events: ReadonlyArray<Event>,
): FleetAgent[] {
  const liveStates = latestLiveStates(events)
  const projected = agents.map((status): FleetAgent => {
    const baselineMillis = Date.parse(status.state_since)
    const live = liveStates.get(status.agent_id)
    if (
      live === undefined ||
      !Number.isFinite(baselineMillis) ||
      live.stateSinceMillis < baselineMillis
    ) {
      return {
        agentID: status.agent_id,
        status,
        state: status.state,
        stateSince: status.state_since,
        ...(status.last_transition === undefined
          ? {}
          : { evidence: status.last_transition }),
      }
    }
    return {
      agentID: status.agent_id,
      status,
      state: live.state,
      stateSince: live.stateSince,
      ...(live.evidence === undefined ? {} : { evidence: live.evidence }),
    }
  })
  projected.sort(compareFleetAgents)
  return projected
}

export function formatBlockedDuration(durationMillis: number): string {
  const totalSeconds = Math.max(0, Math.floor(durationMillis / 1000))
  const seconds = totalSeconds % 60
  const totalMinutes = Math.floor(totalSeconds / 60)
  const minutes = totalMinutes % 60
  const hours = Math.floor(totalMinutes / 60)
  if (hours > 0) {
    return `已等你 ${hours} 小时 ${minutes} 分 ${seconds} 秒`
  }
  if (minutes > 0) {
    return `已等你 ${minutes} 分 ${seconds} 秒`
  }
  return `已等你 ${seconds} 秒`
}

function latestLiveStates(events: ReadonlyArray<Event>): Map<string, LiveState> {
  const states = new Map<string, LiveState>()
  for (const event of events) {
    if (
      event.type !== 'state_changed' ||
      event.agent_id === undefined ||
      event.to === undefined
    ) {
      continue
    }
    const stateSinceMillis = Date.parse(event.timestamp)
    if (!Number.isFinite(stateSinceMillis)) continue
    let state: AgentState
    try {
      state = parseAgentState(event.to, 'state_changed.to')
    } catch {
      continue
    }
    const previous = states.get(event.agent_id)
    if (
      previous !== undefined &&
      previous.stateSinceMillis > stateSinceMillis
    ) {
      continue
    }
    const evidence = parseEventEvidence(event.payload)
    states.set(event.agent_id, {
      state,
      stateSince: event.timestamp,
      stateSinceMillis,
      ...(evidence === undefined ? {} : { evidence }),
    })
  }
  return states
}

function parseEventEvidence(payload: string | undefined): StateEvidence | undefined {
  if (payload === undefined || payload === '') return undefined
  try {
    const decoded: unknown = JSON.parse(payload)
    const object = requireRecord(decoded, 'state_changed.payload')
    const { version, ...evidence } = object
    if (
      typeof version !== 'number' ||
      !Number.isInteger(version) ||
      version < 1 ||
      version > 4
    ) {
      return undefined
    }
    return parseStateEvidence(evidence, 'state_changed.payload')
  } catch {
    return undefined
  }
}

function compareFleetAgents(left: FleetAgent, right: FleetAgent): number {
  const priority = statePriority[left.state] - statePriority[right.state]
  if (priority !== 0) return priority
  if (left.state === 'blocked' && right.state === 'blocked') {
    const wait =
      Date.parse(left.stateSince) - Date.parse(right.stateSince)
    if (wait !== 0) return wait
  }
  const created =
    Date.parse(left.status.created_at) - Date.parse(right.status.created_at)
  if (created !== 0) return created
  return left.agentID.localeCompare(right.agentID)
}
