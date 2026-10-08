import {
  requireArray,
  requireBoolean,
  requireDecimalString,
  requireKeys,
  requireNonNegativeInteger,
  requireRecord,
  requireString,
  requireText,
  requireTimestamp,
} from './parsing'
import type {
  DecimalString,
  RemoteActionContext,
  RemoteActionKind,
  RemoteActionResult,
  RemoteActionScreen,
  RemoteActionTicket,
} from './types'

const maxTicketBytes = 4_096

export function parseActionContext(value: unknown): RemoteActionContext {
  const object = requireRecord(value, 'action context')
  requireKeys(
    object,
    ['state_seq', 'actions', 'tickets', 'expires_at'],
    ['screen'],
  )
  const actions = requireArray(object.actions, 'action context.actions').map(
    (item, index) =>
      parseRemoteActionKind(item, `action context.actions[${index}]`),
  )
  if (actions.length === 0 || actions.length > 3) {
    throw new Error('action context.actions must contain 1..3 entries')
  }
  if (new Set(actions).size !== actions.length) {
    throw new Error('action context.actions must not contain duplicates')
  }

  const tickets = requireArray(
    object.tickets,
    'action context.tickets',
  ).map((item, index) =>
    parseActionTicket(item, `action context.tickets[${index}]`),
  )
  if (
    tickets.length !== actions.length ||
    tickets.some((ticket, index) => ticket.action !== actions[index])
  ) {
    throw new Error('action context.tickets must match actions')
  }

  const stateSeq = requirePositiveDecimal(
    object.state_seq,
    'action context.state_seq',
  )
  const screen =
    object.screen === undefined ? undefined : parseActionScreen(object.screen)
  return {
    stateSeq,
    actions,
    tickets,
    expiresAt: requireTimestamp(
      object.expires_at,
      'action context.expires_at',
    ),
    ...(screen === undefined ? {} : { screen }),
  }
}

export function parseActionResponse(value: unknown): RemoteActionResult {
  const object = requireRecord(value, 'action response')
  requireKeys(object, [
    'state_seq',
    'bytes_written',
    'action_seq',
    'input_seq',
  ])
  const actionSeq = requirePositiveDecimal(
    object.action_seq,
    'action response.action_seq',
  )
  const inputSeq = requirePositiveDecimal(
    object.input_seq,
    'action response.input_seq',
  )
  if (BigInt(inputSeq) !== BigInt(actionSeq) + 1n) {
    throw new Error(
      'action response.input_seq must immediately follow action_seq',
    )
  }
  return {
    stateSeq: requirePositiveDecimal(
      object.state_seq,
      'action response.state_seq',
    ),
    bytesWritten: requireNonNegativeInteger(
      object.bytes_written,
      'action response.bytes_written',
    ),
    actionSeq,
    inputSeq,
  }
}

function parseActionTicket(
  value: unknown,
  name: string,
): RemoteActionTicket {
  const object = requireRecord(value, name)
  requireKeys(object, ['action', 'ticket'])
  const ticket = requireString(object.ticket, `${name}.ticket`)
  if (
    ticket !== ticket.trim() ||
    new TextEncoder().encode(ticket).byteLength > maxTicketBytes
  ) {
    throw new Error(`${name}.ticket must be a bounded opaque string`)
  }
  return {
    action: parseRemoteActionKind(object.action, `${name}.action`),
    ticket,
  }
}

function parseActionScreen(value: unknown): RemoteActionScreen {
  const object = requireRecord(value, 'action context.screen')
  requireKeys(object, ['captured_at', 'rows', 'truncated'])
  return {
    capturedAt: requireTimestamp(
      object.captured_at,
      'action context.screen.captured_at',
    ),
    rows: requireArray(object.rows, 'action context.screen.rows').map(
      (row, index) =>
        requireText(row, `action context.screen.rows[${index}]`),
    ),
    truncated: requireBoolean(
      object.truncated,
      'action context.screen.truncated',
    ),
  }
}

function parseRemoteActionKind(
  value: unknown,
  name: string,
): RemoteActionKind {
  if (value === 'approve' || value === 'deny' || value === 'reply') {
    return value
  }
  throw new Error(`${name} is unsupported`)
}

function requirePositiveDecimal(
  value: unknown,
  name: string,
): DecimalString {
  const parsed = requireDecimalString(value, name)
  if (parsed === '0') {
    throw new Error(`${name} must be positive`)
  }
  return parsed
}
