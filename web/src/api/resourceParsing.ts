import {
  requireArray,
  requireBoolean,
  requireKeys,
  requirePositiveInteger,
  requireRecord,
  requireString,
  requireText,
  requireTimestamp,
} from './parsing'
import type {
  AgentState,
  AgentStatus,
  EventRow,
  HookPolicy,
  HookStatus,
  RunMode,
  ScreenAttribution,
  SignalInjectionMode,
  SignalInjectionReason,
  SignalInjectionStatus,
  StateEvidence,
} from './types'

export function parseAgentList(value: unknown): AgentStatus[] {
  return requireArray(value, 'agents').map((agent, index) =>
    parseAgentStatus(agent, `agents[${index}]`),
  )
}

export function parseAgentStatus(
  value: unknown,
  name = 'agent',
): AgentStatus {
  const object = requireRecord(value, name)
  requireKeys(
    object,
    [
      'agent_id',
      'name',
      'vendor',
      'mode',
      'state',
      'created_at',
      'updated_at',
      'hook_policy',
      'hook_status',
      'signal_injection',
      'signal_injection_status',
      'signal_injection_reason',
      'resumable',
    ],
    [
      'dir',
      'pid',
      'last_error',
      'last_transition',
    ],
  )
  const pid =
    object.pid === undefined
      ? undefined
      : requirePositiveInteger(object.pid, `${name}.pid`)
  const lastTransition =
    object.last_transition === undefined
      ? undefined
      : parseStateEvidence(object.last_transition, `${name}.last_transition`)
  const dir =
    object.dir === undefined
      ? undefined
      : requireString(object.dir, `${name}.dir`)
  return {
    agent_id: requireString(object.agent_id, `${name}.agent_id`),
    name: requireString(object.name, `${name}.name`),
    vendor: requireString(object.vendor, `${name}.vendor`),
    mode: parseRunMode(object.mode, `${name}.mode`),
    state: parseAgentState(object.state, `${name}.state`),
    created_at: requireTimestamp(object.created_at, `${name}.created_at`).iso,
    updated_at: requireTimestamp(object.updated_at, `${name}.updated_at`).iso,
    hook_policy: parseHookPolicy(object.hook_policy, `${name}.hook_policy`),
    hook_status: parseHookStatus(object.hook_status, `${name}.hook_status`),
    signal_injection: parseSignalInjectionMode(
      object.signal_injection,
      `${name}.signal_injection`,
    ),
    signal_injection_status: parseSignalInjectionStatus(
      object.signal_injection_status,
      `${name}.signal_injection_status`,
    ),
    signal_injection_reason: parseSignalInjectionReason(
      object.signal_injection_reason,
      `${name}.signal_injection_reason`,
    ),
    resumable: requireBoolean(object.resumable, `${name}.resumable`),
    ...(dir === undefined ? {} : { dir }),
    ...(pid === undefined ? {} : { pid }),
    ...(object.last_error === undefined
      ? {}
      : { last_error: requireString(object.last_error, `${name}.last_error`) }),
    ...(lastTransition === undefined
      ? {}
      : { last_transition: lastTransition }),
  }
}

export function parseEventRows(value: unknown): EventRow[] {
  return requireArray(value, 'events').map((row, index) => {
    const name = `events[${index}]`
    const object = requireRecord(row, name)
    requireKeys(object, [
      'Seq',
      'Timestamp',
      'Type',
      'SessionID',
      'AgentID',
      'From',
      'To',
      'Reason',
      'Payload',
    ])
    return {
      Seq: requirePositiveInteger(object.Seq, `${name}.Seq`),
      Timestamp: requireTimestamp(object.Timestamp, `${name}.Timestamp`).iso,
      Type: requireString(object.Type, `${name}.Type`),
      SessionID: requireText(object.SessionID, `${name}.SessionID`),
      AgentID: requireText(object.AgentID, `${name}.AgentID`),
      From: requireText(object.From, `${name}.From`),
      To: requireText(object.To, `${name}.To`),
      Reason: requireText(object.Reason, `${name}.Reason`),
      Payload: requireText(object.Payload, `${name}.Payload`),
    }
  })
}

export function parseAgentState(value: unknown, name: string): AgentState {
  if (
    value === 'pending' ||
    value === 'starting' ||
    value === 'working' ||
    value === 'blocked' ||
    value === 'done' ||
    value === 'idle' ||
    value === 'stopped'
  ) {
    return value
  }
  throw new Error(`${name} is unsupported`)
}

function parseStateEvidence(value: unknown, name: string): StateEvidence {
  const object = requireRecord(value, name)
  requireKeys(object, ['source', 'event', 'confidence'], [
    'delivery_id',
    'screen',
  ])
  const source = object.source
  if (
    source !== 'session' &&
    source !== 'process' &&
    source !== 'hook' &&
    source !== 'notify' &&
    source !== 'heuristic' &&
    source !== 'screen' &&
    source !== 'timer' &&
    source !== 'recovery'
  ) {
    throw new Error(`${name}.source is unsupported`)
  }
  if (
    typeof object.confidence !== 'number' ||
    !Number.isFinite(object.confidence) ||
    object.confidence < 0 ||
    object.confidence > 1
  ) {
    throw new Error(`${name}.confidence must be in 0..1`)
  }
  const base = {
    event: requireString(object.event, `${name}.event`),
    confidence: object.confidence,
  }
  switch (source) {
    case 'hook':
    case 'notify':
      if (object.screen !== undefined) {
        throw new Error(`${name}.screen requires screen evidence`)
      }
      return {
        ...base,
        source,
        delivery_id: requireString(
          object.delivery_id,
          `${name}.delivery_id`,
        ),
      }
    case 'screen':
      if (object.delivery_id !== undefined) {
        throw new Error(`${name}.screen cannot contain delivery_id`)
      }
      return {
        ...base,
        source,
        screen: parseScreenAttribution(object.screen, `${name}.screen`),
      }
    case 'session':
    case 'process':
    case 'heuristic':
    case 'timer':
    case 'recovery':
      if (object.delivery_id !== undefined || object.screen !== undefined) {
        throw new Error(`${name}.${source} contains unsupported attribution`)
      }
      return { ...base, source }
    default: {
      const exhaustive: never = source
      throw new Error(`Unsupported evidence source ${String(exhaustive)}`)
    }
  }
}

function parseScreenAttribution(
  value: unknown,
  name: string,
): ScreenAttribution {
  const object = requireRecord(value, name)
  requireKeys(object, [
    'rule',
    'edge',
    'region',
    'output_offset',
    'last_output_seq',
    'evidence',
  ])
  if (object.edge !== 'present' && object.edge !== 'cleared') {
    throw new Error(`${name}.edge is unsupported`)
  }
  return {
    rule: requireString(object.rule, `${name}.rule`),
    edge: object.edge,
    region: requireString(object.region, `${name}.region`),
    output_offset: requirePositiveInteger(
      object.output_offset,
      `${name}.output_offset`,
    ),
    last_output_seq: requirePositiveInteger(
      object.last_output_seq,
      `${name}.last_output_seq`,
    ),
    evidence: requireString(object.evidence, `${name}.evidence`),
  }
}

function parseRunMode(value: unknown, name: string): RunMode {
  if (value === 'interactive' || value === 'oneshot') return value
  throw new Error(`${name} is unsupported`)
}

function parseHookPolicy(value: unknown, name: string): HookPolicy {
  if (value === 'off' || value === 'auto' || value === 'required') return value
  throw new Error(`${name} is unsupported`)
}

function parseHookStatus(value: unknown, name: string): HookStatus {
  if (
    value === 'off' ||
    value === 'awaiting_hook' ||
    value === 'fallback' ||
    value === 'hook_active' ||
    value === 'required_failed' ||
    value === 'detached'
  ) {
    return value
  }
  throw new Error(`${name} is unsupported`)
}

function parseSignalInjectionMode(
  value: unknown,
  name: string,
): SignalInjectionMode {
  if (value === 'auto' || value === 'off') return value
  throw new Error(`${name} is unsupported`)
}

function parseSignalInjectionStatus(
  value: unknown,
  name: string,
): SignalInjectionStatus {
  if (
    value === 'off' ||
    value === 'injected' ||
    value === 'skipped' ||
    value === 'detached'
  ) {
    return value
  }
  throw new Error(`${name} is unsupported`)
}

function parseSignalInjectionReason(
  value: unknown,
  name: string,
): SignalInjectionReason {
  if (
    value === 'hook_policy_off' ||
    value === 'configured_off' ||
    value === 'unsupported' ||
    value === 'relay_unavailable' ||
    value === 'argument_conflict' ||
    value === 'session_config' ||
    value === 'recovered'
  ) {
    return value
  }
  throw new Error(`${name} is unsupported`)
}
