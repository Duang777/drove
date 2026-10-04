import { describe, expect, it, vi } from 'vitest'
import { ManualClock } from './manualClock'

describe('ManualClock', () => {
  it('runs due callbacks in deadline and registration order', () => {
    const clock = new ManualClock(1_000)
    const calls: string[] = []
    clock.setTimeout(() => calls.push('later'), 20)
    clock.setTimeout(() => calls.push('first'), 10)
    clock.setTimeout(() => calls.push('second'), 10)

    clock.advanceBy(10)
    expect(calls).toEqual(['first', 'second'])
    expect(clock.now()).toBe(1_010)

    clock.advanceTo(1_020)
    expect(calls).toEqual(['first', 'second', 'later'])
    expect(clock.pendingCount()).toBe(0)
  })

  it('can cancel a pending callback', () => {
    const clock = new ManualClock()
    const callback = vi.fn()
    const timer = clock.setTimeout(callback, 10)

    clock.clearTimeout(timer)
    clock.advanceBy(10)

    expect(callback).not.toHaveBeenCalled()
  })
})
