import type {
  AgentTerminalView,
  TerminalReplayView,
} from '../terminal/sessionController'

interface Props {
  readonly view: AgentTerminalView
  readonly bindViewport: (element: HTMLDivElement | null) => void
}

interface TerminalNotice {
  readonly kind: 'status' | 'error'
  readonly text: string
}

export function TerminalViewport({
  view,
  bindViewport,
}: Props) {
  const replaying = view.replay.kind !== 'live'
  const notice = terminalNotice(view)

  return (
    <section
      className="terminal-panel"
      aria-label={replaying ? '回放终端' : '实时终端'}
    >
      <div className="terminal-panel-bar">
        <span className="terminal-mode">
          <span
            className={`terminal-mode-dot ${replaying ? 'is-replay' : 'is-live'}`}
            aria-hidden="true"
          />
          {replaying ? '历史回放' : '实时终端'}
        </span>
        <span className="terminal-input-state">
          {view.inputEnabled ? '可输入' : replaying ? '只读回放' : '输入暂不可用'}
        </span>
      </div>

      {notice !== null && (
        <p
          className={`terminal-notice terminal-notice-${notice.kind}`}
          role={notice.kind === 'error' ? 'alert' : 'status'}
        >
          {notice.text}
        </p>
      )}

      <div
        className={`terminal-viewport-scroll ${replaying ? 'terminal-replay-scroll' : ''}`}
      >
        <div
          ref={bindViewport}
          className="terminal-viewport"
          data-terminal-mode={replaying ? 'replay' : 'live'}
        />
      </div>

      <FramePreview view={view} />
    </section>
  )
}

function FramePreview({ view }: { readonly view: AgentTerminalView }) {
  if (
    !view.frameLoading &&
    view.framePreview === null &&
    view.frameError === null
  ) {
    return null
  }

  return (
    <section className="frame-preview" aria-labelledby="frame-preview-title">
      <div className="frame-preview-heading">
        <h3 id="frame-preview-title">帧预览</h3>
        {view.framePreview !== null && (
          <span>
            {view.framePreview.columns} × {view.framePreview.rows}
          </span>
        )}
      </div>
      {view.frameLoading ? (
        <p className="frame-preview-status" role="status">
          正在加载可见帧
        </p>
      ) : view.frameError !== null ? (
        <p className="frame-preview-status is-error" role="alert">
          {view.frameError}
        </p>
      ) : view.framePreview !== null ? (
        <div className="frame-preview-scroll">
          <pre aria-label="只读帧预览">
            {view.framePreview.lines.join('\n')}
          </pre>
        </div>
      ) : null}
    </section>
  )
}

function terminalNotice(view: AgentTerminalView): TerminalNotice | null {
  switch (view.connection) {
    case 'loading':
      return { kind: 'status', text: '正在加载时间线' }
    case 'connecting':
      return { kind: 'status', text: '正在连接终端' }
    case 'syncing':
      return { kind: 'status', text: '正在同步录制' }
    case 'reconnecting':
      return {
        kind: 'status',
        text:
          view.connectionError === null
            ? '连接中断，正在重连'
            : `连接中断，正在重连：${view.connectionError}`,
      }
    case 'error':
      return {
        kind: 'error',
        text:
          view.connectionError === null
            ? '终端连接失败'
            : `终端连接失败：${view.connectionError}`,
      }
    case 'disposed':
      return { kind: 'status', text: '终端已关闭' }
    case 'live':
      return replayNotice(view.replay)
    default: {
      const exhaustive: never = view.connection
      return exhaustive
    }
  }
}

function replayNotice(replay: TerminalReplayView): TerminalNotice | null {
  switch (replay.kind) {
    case 'live':
    case 'ready':
      return null
    case 'building':
      return { kind: 'status', text: '正在构建精确回放' }
    case 'not_ready':
      return { kind: 'status', text: '该时间点尚无完整录制' }
    case 'expired':
      return { kind: 'error', text: '该时间段的终端输出已过期' }
    case 'local_limit':
      return {
        kind: 'error',
        text: `本地回放已达到 ${formatBytes(replay.byteLimit)} 上限`,
      }
    case 'error':
      return { kind: 'error', text: `回放失败：${replay.message}` }
    default: {
      const exhaustive: never = replay
      return exhaustive
    }
  }
}

function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${Math.round(bytes / 1024)} KiB`
  return `${Math.round(bytes / (1024 * 1024))} MiB`
}
