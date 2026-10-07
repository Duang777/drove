import { describe, expect, it } from 'vitest'
import { resolveFleetKeyboardAction } from './fleetKeyboard'

describe('fleet keyboard navigation', () => {
  it('opens the sorted agent assigned to number keys 1 through 9', () => {
    const orderedAgentIDs = ['blocked-old', 'working', 'idle']

    expect(
      resolveFleetKeyboardAction({
        location: { kind: 'fleet' },
        key: '2',
        hasModifier: false,
        editableTarget: false,
        orderedAgentIDs,
      }),
    ).toEqual({ kind: 'open_agent', agentID: 'working' })
    expect(
      resolveFleetKeyboardAction({
        location: { kind: 'fleet' },
        key: '4',
        hasModifier: false,
        editableTarget: false,
        orderedAgentIDs,
      }),
    ).toBeNull()
  })

  it('does not intercept number keys while the operator edits a control', () => {
    expect(
      resolveFleetKeyboardAction({
        location: { kind: 'fleet' },
        key: '1',
        hasModifier: false,
        editableTarget: true,
        orderedAgentIDs: ['agent-1'],
      }),
    ).toBeNull()
    expect(
      resolveFleetKeyboardAction({
        location: { kind: 'fleet' },
        key: '1',
        hasModifier: true,
        editableTarget: false,
        orderedAgentIDs: ['agent-1'],
      }),
    ).toBeNull()
  })

  it('returns from agent detail on Escape', () => {
    expect(
      resolveFleetKeyboardAction({
        location: { kind: 'agent', agentID: 'agent-1' },
        key: 'Escape',
        hasModifier: false,
        editableTarget: true,
        orderedAgentIDs: [],
      }),
    ).toEqual({ kind: 'back_to_fleet' })
  })
})
