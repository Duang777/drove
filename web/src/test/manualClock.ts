interface ScheduledTask {
  readonly id: number
  readonly dueAt: number
  readonly callback: () => void
}

/**
 * Deterministic timer fixture for reconnect and playback controller tests.
 */
export class ManualClock {
  private currentMillis: number
  private nextID = 1
  private readonly tasks = new Map<number, ScheduledTask>()

  constructor(startMillis = 0) {
    if (!Number.isSafeInteger(startMillis)) {
      throw new Error('ManualClock start must be a safe integer')
    }
    this.currentMillis = startMillis
  }

  now(): number {
    return this.currentMillis
  }

  setTimeout(callback: () => void, delayMillis: number): number {
    if (!Number.isSafeInteger(delayMillis) || delayMillis < 0) {
      throw new Error('ManualClock delay must be a non-negative safe integer')
    }
    const id = this.nextID
    this.nextID += 1
    this.tasks.set(id, {
      id,
      dueAt: this.currentMillis + delayMillis,
      callback,
    })
    return id
  }

  clearTimeout(id: number): void {
    this.tasks.delete(id)
  }

  advanceBy(delayMillis: number): void {
    if (!Number.isSafeInteger(delayMillis) || delayMillis < 0) {
      throw new Error('ManualClock advance must be a non-negative safe integer')
    }
    this.advanceTo(this.currentMillis + delayMillis)
  }

  advanceTo(targetMillis: number): void {
    if (!Number.isSafeInteger(targetMillis) || targetMillis < this.currentMillis) {
      throw new Error('ManualClock target cannot precede the current time')
    }

    while (true) {
      const task = this.nextTask(targetMillis)
      if (task === undefined) break
      this.currentMillis = task.dueAt
      this.tasks.delete(task.id)
      task.callback()
    }
    this.currentMillis = targetMillis
  }

  pendingCount(): number {
    return this.tasks.size
  }

  private nextTask(targetMillis: number): ScheduledTask | undefined {
    let next: ScheduledTask | undefined
    for (const task of this.tasks.values()) {
      if (
        task.dueAt <= targetMillis &&
        (next === undefined ||
          task.dueAt < next.dueAt ||
          (task.dueAt === next.dueAt && task.id < next.id))
      ) {
        next = task
      }
    }
    return next
  }
}
