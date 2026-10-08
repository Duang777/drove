import { requireDecimalString } from './api/parsing'
import type { DecimalString } from './api/types'

export type AppLocation =
  | { readonly kind: 'fleet' }
  | {
      readonly kind: 'agent'
      readonly agentID: string
      readonly blockedSeq?: DecimalString
    }

export function parseAppLocation(search: string): AppLocation {
  const query = new URLSearchParams(search)
  const agentID = query.get('agent')
  if (agentID === null || agentID.length === 0) {
    return { kind: 'fleet' }
  }
  const blockedSeq = parseBlockedSeq(query.get('blocked'))
  return {
    kind: 'agent',
    agentID,
    ...(blockedSeq === undefined ? {} : { blockedSeq }),
  }
}

export function appLocationHref(location: AppLocation): string {
  switch (location.kind) {
    case 'fleet':
      return '/'
    case 'agent': {
      const query = new URLSearchParams({ agent: location.agentID })
      if (location.blockedSeq !== undefined) {
        query.set('blocked', location.blockedSeq)
      }
      return `/?${query.toString()}`
    }
    default: {
      const exhaustive: never = location
      throw new Error(`Unsupported app location ${String(exhaustive)}`)
    }
  }
}

function parseBlockedSeq(value: string | null): DecimalString | undefined {
  if (value === null) return undefined
  try {
    const parsed = requireDecimalString(value, 'blocked')
    return parsed === '0' ? undefined : parsed
  } catch {
    return undefined
  }
}
