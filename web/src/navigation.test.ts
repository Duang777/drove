import { describe, expect, it } from 'vitest'
import { appLocationHref, parseAppLocation } from './navigation'

describe('app navigation', () => {
  it('parses fleet and agent query locations', () => {
    expect(parseAppLocation('')).toEqual({ kind: 'fleet' })
    expect(parseAppLocation('?agent=')).toEqual({ kind: 'fleet' })
    expect(parseAppLocation('?agent=session%2Falpha')).toEqual({
      kind: 'agent',
      agentID: 'session/alpha',
    })
  })

  it('writes stable root-relative locations', () => {
    expect(appLocationHref({ kind: 'fleet' })).toBe('/')
    expect(
      appLocationHref({ kind: 'agent', agentID: 'session/alpha beta' }),
    ).toBe('/?agent=session%2Falpha+beta')
  })
})
