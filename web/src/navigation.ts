export type AppLocation =
  | { readonly kind: 'fleet' }
  | { readonly kind: 'agent'; readonly agentID: string }

export function parseAppLocation(search: string): AppLocation {
  const agentID = new URLSearchParams(search).get('agent')
  if (agentID === null || agentID.length === 0) {
    return { kind: 'fleet' }
  }
  return { kind: 'agent', agentID }
}

export function appLocationHref(location: AppLocation): string {
  switch (location.kind) {
    case 'fleet':
      return '/'
    case 'agent': {
      const query = new URLSearchParams({ agent: location.agentID })
      return `/?${query.toString()}`
    }
    default: {
      const exhaustive: never = location
      throw new Error(`Unsupported app location ${String(exhaustive)}`)
    }
  }
}
