import { describe, expect, it } from 'vitest'
import { formatUint64 } from './api/parsing'
import { appLocationHref, parseAppLocation } from './navigation'

describe('app navigation', () => {
  it('parses fleet and agent query locations', () => {
    expect(parseAppLocation('')).toEqual({ kind: 'fleet' })
    expect(parseAppLocation('?agent=')).toEqual({ kind: 'fleet' })
    expect(parseAppLocation('?agent=session%2Falpha')).toEqual({
      kind: 'agent',
      agentID: 'session/alpha',
    })
    expect(
      parseAppLocation(
        '?agent=session%2Falpha&blocked=9007199254740993',
      ),
    ).toEqual({
      kind: 'agent',
      agentID: 'session/alpha',
      blockedSeq: '9007199254740993',
    })
  })

  it('writes stable root-relative locations', () => {
    expect(appLocationHref({ kind: 'fleet' })).toBe('/')
    expect(
      appLocationHref({ kind: 'agent', agentID: 'session/alpha beta' }),
    ).toBe('/?agent=session%2Falpha+beta')
    expect(
      appLocationHref({
        kind: 'agent',
        agentID: 'session/alpha beta',
        blockedSeq: formatUint64(9_007_199_254_740_993n),
      }),
    ).toBe(
      '/?agent=session%2Falpha+beta&blocked=9007199254740993',
    )
  })

  it('ignores malformed or zero Blocked sequence selectors', () => {
    for (const blocked of ['0', '01', '-1', '18446744073709551616']) {
      expect(
        parseAppLocation(`?agent=agent-1&blocked=${blocked}`),
      ).toEqual({
        kind: 'agent',
        agentID: 'agent-1',
      })
    }
  })
})
