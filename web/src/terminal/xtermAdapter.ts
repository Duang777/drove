import { FitAddon } from '@xterm/addon-fit'
import { Terminal } from '@xterm/xterm'

export interface TerminalDimensions {
  readonly rows: number
  readonly columns: number
}

export interface TerminalDisposable {
  dispose(): void
}

export interface TerminalAdapter {
  mount(viewport: HTMLDivElement | null): void
  write(data: Uint8Array): Promise<void>
  resize(dimensions: TerminalDimensions): Promise<void>
  onInput(listener: (data: string) => void): TerminalDisposable
  proposeDimensions(): TerminalDimensions | undefined
  focus(): void
  dispose(): void
}

export interface TerminalAdapterOptions {
  readonly kind: 'live' | 'replay'
  readonly rows: number
  readonly columns: number
}

export type TerminalAdapterFactory = (
  options: TerminalAdapterOptions,
) => TerminalAdapter

interface PendingWrite {
  readonly resolve: () => void
  readonly reject: (error: Error) => void
}

class XtermAdapter implements TerminalAdapter {
  private readonly terminal: Terminal
  private readonly fitAddon = new FitAddon()
  private readonly pendingWrites = new Set<PendingWrite>()
  private host: HTMLDivElement | null = null
  private opened = false
  private disposed = false

  constructor(options: TerminalAdapterOptions) {
    this.terminal = new Terminal({
      rows: options.rows,
      cols: options.columns,
      allowProposedApi: true,
      convertEol: false,
      cursorBlink: options.kind === 'live',
      disableStdin: options.kind === 'replay',
      scrollback: options.kind === 'live' ? 10_000 : 0,
      theme: {
        background: '#101418',
        foreground: '#e5e7eb',
        cursor: '#34d399',
        selectionBackground: '#47556980',
      },
    })
    this.terminal.loadAddon(this.fitAddon)
  }

  mount(viewport: HTMLDivElement | null): void {
    if (this.disposed) return
    if (viewport === null) {
      this.host?.remove()
      return
    }
    if (this.host === null) {
      this.host = document.createElement('div')
      this.host.className = 'terminal-xterm-host'
    }
    if (this.host.parentElement !== viewport) {
      viewport.append(this.host)
    }
    if (!this.opened) {
      this.terminal.open(this.host)
      this.opened = true
    }
  }

  write(data: Uint8Array): Promise<void> {
    if (this.disposed) {
      return Promise.reject(new Error('Terminal adapter is disposed'))
    }
    return new Promise<void>((resolve, reject) => {
      const pending: PendingWrite = {
        resolve: () => {
          this.pendingWrites.delete(pending)
          resolve()
        },
        reject,
      }
      this.pendingWrites.add(pending)
      try {
        this.terminal.write(data, pending.resolve)
      } catch (error) {
        this.pendingWrites.delete(pending)
        reject(error instanceof Error ? error : new Error(String(error)))
      }
    })
  }

  resize(dimensions: TerminalDimensions): Promise<void> {
    if (this.disposed) {
      return Promise.reject(new Error('Terminal adapter is disposed'))
    }
    this.terminal.resize(dimensions.columns, dimensions.rows)
    return Promise.resolve()
  }

  onInput(listener: (data: string) => void): TerminalDisposable {
    if (this.disposed) {
      return { dispose() {} }
    }
    return this.terminal.onData(listener)
  }

  proposeDimensions(): TerminalDimensions | undefined {
    if (this.disposed || !this.opened) return undefined
    const dimensions = this.fitAddon.proposeDimensions()
    if (
      dimensions === undefined ||
      dimensions.rows <= 0 ||
      dimensions.cols <= 0
    ) {
      return undefined
    }
    return { rows: dimensions.rows, columns: dimensions.cols }
  }

  focus(): void {
    if (!this.disposed && this.opened) this.terminal.focus()
  }

  dispose(): void {
    if (this.disposed) return
    this.disposed = true
    this.host?.remove()
    this.host = null
    const error = new Error('Terminal adapter disposed during write')
    for (const pending of this.pendingWrites) pending.reject(error)
    this.pendingWrites.clear()
    this.terminal.dispose()
  }
}

export const createXtermAdapter: TerminalAdapterFactory = (options) =>
  new XtermAdapter(options)
