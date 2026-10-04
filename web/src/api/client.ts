/**
 * Drove daemon REST client.
 *
 * Every response crosses one parser before application code can observe it.
 */
import { formatUint64, requireRecord, requireString } from './parsing'
import {
  parseFramePreview,
  parseOutputExpiry,
  parseTimeline,
} from './replayParsing'
import {
  parseAgentList,
  parseAgentStatus,
  parseEventRows,
} from './resourceParsing'
import type {
  AgentStatus,
  EventRow,
  FrameSelector,
  OutputRange,
  StartRequest,
  TerminalFramePreview,
  TerminalTimeline,
} from './types'

export { parseFramePreview, parseTimeline } from './replayParsing'

const BASE = '/api/v1'

export class OutputExpiredError extends Error {
  readonly kind = 'output_expired'

  constructor(
    readonly sessionID: string,
    readonly missing: ReadonlyArray<OutputRange>,
  ) {
    super('Recorded terminal output has expired')
    this.name = 'OutputExpiredError'
  }
}

/** List every session. */
export function listAgents(): Promise<AgentStatus[]> {
  return requestJSON('/agents', parseAgentList)
}

/** Start one session. */
export function startAgent(req: StartRequest): Promise<AgentStatus> {
  return requestJSON('/agents', parseAgentStatus, {
    method: 'POST',
    body: JSON.stringify(req),
  })
}

/** Stop one session. */
export function stopAgent(id: string): Promise<void> {
  return requestNoContent(`/agents/${encodeURIComponent(id)}`, {
    method: 'DELETE',
  })
}

/** Replay one session's event envelopes. */
export function replayAgent(id: string): Promise<EventRow[]> {
  return requestJSON(
    `/agents/${encodeURIComponent(id)}/events`,
    parseEventRows,
  )
}

/** Load the authoritative state and retention timeline. */
export function getAgentTimeline(
  id: string,
  signal?: AbortSignal,
): Promise<TerminalTimeline> {
  return requestJSON(
    `/agents/${encodeURIComponent(id)}/timeline`,
    parseTimeline,
    { signal },
  )
}

/** Load a visible frame preview. The result cannot seed exact replay. */
export async function getAgentFrame(
  id: string,
  selector: FrameSelector,
  signal?: AbortSignal,
): Promise<TerminalFramePreview> {
  const query = frameSelectorQuery(selector)
  const path = `/agents/${encodeURIComponent(id)}/frame?${query}`
  const response = await fetch(`${BASE}${path}`, {
    headers: { 'Content-Type': 'application/json' },
    signal,
  })
  const body = await readJSON(response)
  if (response.status === 410) {
    const expiry = parseOutputExpiry(body)
    throw new OutputExpiredError(expiry.sessionID, expiry.missing)
  }
  if (!response.ok) {
    throw responseError(response, body)
  }
  return parseFramePreview(body)
}

async function requestJSON<T>(
  path: string,
  parse: (value: unknown) => T,
  init?: RequestInit,
): Promise<T> {
  const response = await fetch(`${BASE}${path}`, {
    headers: { 'Content-Type': 'application/json' },
    ...init,
  })
  const body = await readJSON(response)
  if (!response.ok) {
    throw responseError(response, body)
  }
  if (response.status === 204) {
    throw new Error(`Expected JSON response from ${path}`)
  }
  return parse(body)
}

async function requestNoContent(
  path: string,
  init?: RequestInit,
): Promise<void> {
  const response = await fetch(`${BASE}${path}`, {
    headers: { 'Content-Type': 'application/json' },
    ...init,
  })
  if (!response.ok) {
    throw responseError(response, await readJSON(response))
  }
  if (response.status !== 204) {
    throw new Error(`Expected 204 response from ${path}`)
  }
}

function frameSelectorQuery(selector: FrameSelector): string {
  const query = new URLSearchParams()
  switch (selector.kind) {
    case 'sequence':
      query.set('seq', formatUint64(selector.seq, 'frame sequence'))
      break
    case 'timestamp':
      query.set('at', selector.at.iso)
      break
    case 'offset':
      query.set('offset', formatUint64(selector.offset, 'frame offset'))
      break
    default: {
      const exhaustive: never = selector
      throw new Error(`Unsupported frame selector ${String(exhaustive)}`)
    }
  }
  return query.toString()
}

async function readJSON(response: Response): Promise<unknown> {
  try {
    const value: unknown = await response.json()
    return value
  } catch {
    return undefined
  }
}

function responseError(response: Response, body: unknown): Error {
  if (body !== undefined) {
    try {
      const object = requireRecord(body, 'error response')
      const message = requireString(object.error, 'error response.error')
      return new Error(message)
    } catch {
      // Fall through to the HTTP status when the daemon body is malformed.
    }
  }
  return new Error(`${response.status} ${response.statusText}`)
}
