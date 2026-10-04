import { describe, expect, expectTypeOf, it } from 'vitest'
import type {
  TerminalFramePreview,
  TerminalSnapshot,
} from '../api/types'
import {
  outputOperation,
  outputRange,
  recordedTimestamp,
  recordingCursor,
  resizeOperation,
} from '../test/terminalFixtures'
import {
  SessionTape,
  type RecordedOperation,
} from './sessionTape'

describe('SessionTape', () => {
  it('advances the exact frontier only after operation and timestamp arrive', () => {
    const tape = new SessionTape()
    const output = outputOperation({
      seq: 2n,
      offset: 0n,
      data: [0x68, 0x69],
    })

    expect(tape.recordOperation(output)).toEqual({
      kind: 'accepted',
      exactFrontier: recordingCursor(0n, 0n),
    })
    expect(tape.plan({ target: output.cursor })).toEqual({
      kind: 'not_ready',
    })

    expect(tape.recordTimestamp(recordedTimestamp(2n, 2_000))).toEqual({
      kind: 'accepted',
      exactFrontier: output.cursor,
    })
    expect(tape.plan({ target: output.cursor })).toEqual({
      kind: 'exact',
      target: output.cursor,
      operations: [{ operation: output, atMillis: 2_000 }],
    })
  })

  it('correlates timestamps that arrive before raw operations', () => {
    const tape = new SessionTape()
    const resize = resizeOperation({ seq: 3n, nextOffset: 0n })

    tape.recordTimestamp(recordedTimestamp(3n, 3_000))
    expect(tape.exactFrontier()).toEqual(recordingCursor(0n, 0n))

    expect(tape.recordOperation(resize)).toEqual({
      kind: 'accepted',
      exactFrontier: resize.cursor,
    })
  })

  it('accepts identical reconnect duplicates without storing them twice', () => {
    const tape = new SessionTape()
    const output = outputOperation({
      seq: 4n,
      offset: 0n,
      data: [1, 2, 3],
    })

    tape.recordOperation(output)
    tape.recordTimestamp(recordedTimestamp(4n, 4_000))

    expect(tape.recordOperation(output)).toEqual({
      kind: 'duplicate',
      exactFrontier: output.cursor,
    })
    expect(tape.recordTimestamp(recordedTimestamp(4n, 4_000))).toEqual({
      kind: 'duplicate',
      exactFrontier: output.cursor,
    })
    expect(tape.storedBytes()).toBe(3)
    expectExactOperationCount(tape, output.cursor, 1)
  })

  it('copies origin bytes on ingest and replay', () => {
    const tape = new SessionTape()
    const output = outputOperation({
      seq: 4n,
      offset: 0n,
      data: [1, 2, 3],
    })
    tape.recordOperation(output)
    tape.recordTimestamp(recordedTimestamp(4n, 4_000))
    output.data[0] = 9

    const firstPlan = tape.plan({ target: output.cursor })
    expect(firstPlan.kind).toBe('exact')
    if (
      firstPlan.kind !== 'exact' ||
      firstPlan.operations[0]?.operation.kind !== 'output'
    ) {
      throw new Error('expected one exact output operation')
    }
    expect(firstPlan.operations[0].operation.data).toEqual(
      Uint8Array.from([1, 2, 3]),
    )
    firstPlan.operations[0].operation.data[1] = 9

    const secondPlan = tape.plan({ target: output.cursor })
    expect(secondPlan.kind).toBe('exact')
    if (
      secondPlan.kind !== 'exact' ||
      secondPlan.operations[0]?.operation.kind !== 'output'
    ) {
      throw new Error('expected one exact output operation')
    }
    expect(secondPlan.operations[0].operation.data).toEqual(
      Uint8Array.from([1, 2, 3]),
    )
  })

  it('rejects conflicting duplicates, output gaps, and sequence regressions', () => {
    const tape = new SessionTape()
    const first = outputOperation({
      seq: 5n,
      offset: 0n,
      data: [1, 2],
    })
    tape.recordOperation(first)

    expect(
      tape.recordOperation(
        outputOperation({ seq: 5n, offset: 0n, data: [1, 3] }),
      ),
    ).toEqual({ kind: 'rejected', reason: 'conflicting_duplicate' })
    expect(
      tape.recordOperation(
        outputOperation({ seq: 6n, offset: 3n, data: [4] }),
      ),
    ).toEqual({ kind: 'rejected', reason: 'output_gap' })
    expect(
      tape.recordOperation(
        resizeOperation({ seq: 4n, nextOffset: 2n }),
      ),
    ).toEqual({ kind: 'rejected', reason: 'sequence_regression' })
  })

  it('rejects event time regression without changing the exact prefix', () => {
    const tape = new SessionTape()
    const first = outputOperation({
      seq: 2n,
      offset: 0n,
      data: [1],
    })
    const second = outputOperation({
      seq: 4n,
      offset: 1n,
      data: [2],
    })
    tape.recordOperation(first)
    tape.recordOperation(second)
    tape.recordTimestamp(recordedTimestamp(2n, 2_000))

    expect(tape.recordTimestamp(recordedTimestamp(4n, 1_999))).toEqual({
      kind: 'rejected',
      reason: 'time_regression',
    })
    expect(tape.exactFrontier()).toEqual(first.cursor)
  })

  it('freezes before the byte limit and retains the exact origin prefix', () => {
    const tape = new SessionTape({ byteLimit: 3 })
    const retained = outputOperation({
      seq: 2n,
      offset: 0n,
      data: [1, 2],
    })
    const rejected = outputOperation({
      seq: 3n,
      offset: 2n,
      data: [3, 4],
    })
    tape.recordOperation(retained)
    tape.recordTimestamp(recordedTimestamp(2n, 2_000))

    expect(tape.recordOperation(rejected)).toEqual({
      kind: 'local_limit',
      byteLimit: 3,
    })
    expect(tape.reachedLocalLimit()).toBe(true)
    expect(tape.storedBytes()).toBe(2)
    expect(tape.recordOperation(retained).kind).toBe('duplicate')
    expect(tape.plan({ target: retained.cursor }).kind).toBe('exact')
    expect(tape.plan({ target: rejected.cursor })).toEqual({
      kind: 'local_limit',
      byteLimit: 3,
    })
    tape.recordTimestamp(recordedTimestamp(3n, 3_000))
    expect(tape.planAt({ atMillis: 3_000 })).toEqual({
      kind: 'local_limit',
      byteLimit: 3,
    })
  })

  it('reports expired output before attempting a local replay', () => {
    const tape = new SessionTape()
    const output = outputOperation({
      seq: 2n,
      offset: 0n,
      data: [1, 2],
    })
    tape.recordOperation(output)
    tape.recordTimestamp(recordedTimestamp(2n, 2_000))

    expect(
      tape.plan({
        target: output.cursor,
        missing: [outputRange(0n, 1n), outputRange(4n, 8n)],
      }),
    ).toEqual({
      kind: 'expired',
      missing: [outputRange(0n, 1n)],
    })
  })

  it('plans by recorded time without deriving state spans', () => {
    const tape = new SessionTape()
    const first = outputOperation({
      seq: 2n,
      offset: 0n,
      data: [1],
    })
    const second = resizeOperation({ seq: 5n, nextOffset: 1n })
    tape.recordOperation(first)
    tape.recordOperation(second)
    tape.recordTimestamp(recordedTimestamp(2n, 2_000))
    tape.recordTimestamp(recordedTimestamp(5n, 5_000))

    expect(tape.planAt({ atMillis: 1_999 })).toEqual({
      kind: 'exact',
      target: recordingCursor(0n, 0n),
      operations: [],
    })
    expectExactOperationCount(tape, second.cursor, 2)
    expect(tape.planAt({ atMillis: 5_001 })).toEqual({
      kind: 'not_ready',
    })
  })

  it('does not admit visible previews as recorded operations', () => {
    expectTypeOf<TerminalFramePreview>().not.toExtend<RecordedOperation>()
    expectTypeOf<TerminalSnapshot>().not.toExtend<RecordedOperation>()
    expectTypeOf<
      Parameters<SessionTape['recordOperation']>[0]
    >().toEqualTypeOf<RecordedOperation>()
  })
})

function expectExactOperationCount(
  tape: SessionTape,
  target: { readonly seq: bigint; readonly nextOffset: bigint },
  count: number,
): void {
  const plan = tape.plan({ target })
  expect(plan.kind).toBe('exact')
  if (plan.kind === 'exact') {
    expect(plan.operations).toHaveLength(count)
  }
}
