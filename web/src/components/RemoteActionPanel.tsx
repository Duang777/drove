import {
  Ban,
  Check,
  MessageSquareText,
  RefreshCw,
  ShieldCheck,
} from 'lucide-react'
import { useCallback, useEffect, useRef, useState } from 'react'
import {
  executeAgentAction,
  getAgentActionContext,
} from '../api/client'
import type {
  DecimalString,
  RemoteActionContext,
  RemoteActionKind,
  RemoteActionResult,
} from '../api/types'
import { currentPushDeviceID } from '../notifications/push'

const maxReplyBytes = 4_096
const controlCharacters = /[\u0000-\u001f\u007f-\u009f]/

type PanelState =
  | { readonly kind: 'loading' }
  | { readonly kind: 'ready'; readonly context: RemoteActionContext }
  | { readonly kind: 'submitting'; readonly action: RemoteActionKind }
  | { readonly kind: 'sent' }
  | { readonly kind: 'unavailable'; readonly message: string }
  | { readonly kind: 'failed'; readonly message: string }

export interface RemoteActionPanelProps {
  readonly agentID: string
  readonly blockedSeq: DecimalString
  readonly resolveDeviceID?: () => string | null
  readonly loadContext?: typeof getAgentActionContext
  readonly submitAction?: typeof executeAgentAction
  readonly onSubmitted?: (result: RemoteActionResult) => void
}

export function RemoteActionPanel({
  agentID,
  blockedSeq,
  resolveDeviceID = currentPushDeviceID,
  loadContext = getAgentActionContext,
  submitAction = executeAgentAction,
  onSubmitted,
}: RemoteActionPanelProps) {
  const [state, setState] = useState<PanelState>({ kind: 'loading' })
  const [reply, setReply] = useState('')
  const [replyError, setReplyError] = useState<string | null>(null)
  const [confirmingApproval, setConfirmingApproval] = useState(false)
  const [reloadVersion, setReloadVersion] = useState(0)
  const submitController = useRef<AbortController | null>(null)

  const reload = useCallback(() => {
    setReply('')
    setReplyError(null)
    setConfirmingApproval(false)
    setState({ kind: 'loading' })
    setReloadVersion((version) => version + 1)
  }, [])

  useEffect(() => {
    const deviceID = resolveDeviceID()
    if (deviceID === null) {
      setState({
        kind: 'unavailable',
        message:
          '此设备尚未启用浏览器通知，无法获取远程操作授权。',
      })
      return
    }
    const controller = new AbortController()
    setState({ kind: 'loading' })
    void loadContext(
      agentID,
      blockedSeq,
      deviceID,
      controller.signal,
    )
      .then((context) => {
        if (context.stateSeq !== blockedSeq) {
          throw new Error('远程操作上下文与当前阻塞状态不一致')
        }
        setState({ kind: 'ready', context })
      })
      .catch((error: unknown) => {
        if (controller.signal.aborted) return
        setState({ kind: 'failed', message: errorMessage(error) })
      })
    return () => {
      controller.abort()
    }
  }, [
    agentID,
    blockedSeq,
    loadContext,
    reloadVersion,
    resolveDeviceID,
  ])

  useEffect(
    () => () => {
      submitController.current?.abort()
    },
    [],
  )

  async function send(
    action: RemoteActionKind,
    replyText = '',
  ): Promise<void> {
    if (state.kind !== 'ready') return
    const authorization = state.context.tickets.find(
      (ticket) => ticket.action === action,
    )
    if (authorization === undefined) {
      setState({
        kind: 'failed',
        message: '该操作没有可用授权，请重新加载。',
      })
      setReply('')
      return
    }

    setState({ kind: 'submitting', action })
    setReply('')
    setReplyError(null)
    setConfirmingApproval(false)
    const controller = new AbortController()
    submitController.current = controller
    try {
      const result = await submitAction(
        agentID,
        authorization.ticket,
        replyText,
        controller.signal,
      )
      setState({ kind: 'sent' })
      onSubmitted?.(result)
    } catch (error) {
      if (controller.signal.aborted) return
      setState({ kind: 'failed', message: errorMessage(error) })
    } finally {
      submitController.current = null
      setReply('')
    }
  }

  function sendReply(): void {
    const validation = validateActionReply(reply)
    if (validation.kind === 'invalid') {
      setReplyError(validation.message)
      return
    }
    void send('reply', validation.reply)
  }

  return (
    <section
      className="remote-action-panel"
      aria-labelledby="remote-action-title"
    >
      <div className="remote-action-heading">
        <div>
          <h3 id="remote-action-title">
            <ShieldCheck size={16} aria-hidden="true" />
            等待你的决定
          </h3>
          <p>操作会写入当前终端，并记录脱敏审计事件。</p>
        </div>
        <code>blocked #{blockedSeq}</code>
      </div>

      {state.kind === 'loading' && (
        <p className="remote-action-status" role="status">
          正在读取当前请求…
        </p>
      )}
      {state.kind === 'unavailable' && (
        <p className="remote-action-status" role="status">
          {state.message}
        </p>
      )}
      {state.kind === 'failed' && (
        <div className="remote-action-result">
          <p className="inline-error" role="alert">
            {state.message}
          </p>
          <button
            type="button"
            className="button button-secondary button-with-icon"
            aria-label="重新加载远程操作"
            onClick={reload}
          >
            <RefreshCw size={15} aria-hidden="true" />
            重新加载
          </button>
        </div>
      )}
      {state.kind === 'submitting' && (
        <p className="remote-action-status" role="status">
          正在{actionProgressLabel(state.action)}…
        </p>
      )}
      {state.kind === 'sent' && (
        <p className="remote-action-sent" role="status">
          <Check size={16} aria-hidden="true" />
          已发送，等待 Agent 状态更新
        </p>
      )}
      {state.kind === 'ready' && (
        <>
          {state.context.screen !== undefined && (
            <div className="remote-action-screen">
              <div className="remote-action-screen-meta">
                <span>当前终端</span>
                <time dateTime={state.context.screen.capturedAt.iso}>
                  {formatCapturedAt(state.context.screen.capturedAt.epochMillis)}
                </time>
              </div>
              <pre>{state.context.screen.rows.join('\n')}</pre>
              {state.context.screen.truncated && (
                <span className="remote-action-truncated">
                  仅显示末尾内容
                </span>
              )}
            </div>
          )}

          <div className="remote-action-controls">
            {state.context.actions.includes('approve') &&
              (confirmingApproval ? (
                <div className="remote-action-confirm" role="group">
                  <span>确认批准当前请求？</span>
                  <button
                    type="button"
                    className="button button-primary"
                    aria-label="确认批准请求"
                    onClick={() => void send('approve')}
                  >
                    确认批准
                  </button>
                  <button
                    type="button"
                    className="button button-secondary"
                    onClick={() => setConfirmingApproval(false)}
                  >
                    取消
                  </button>
                </div>
              ) : (
                <button
                  type="button"
                  className="button button-primary button-with-icon"
                  aria-label="批准请求"
                  onClick={() => setConfirmingApproval(true)}
                >
                  <ShieldCheck size={15} aria-hidden="true" />
                  批准
                </button>
              ))}
            {state.context.actions.includes('deny') && (
              <button
                type="button"
                className="button button-danger button-with-icon"
                aria-label="拒绝请求"
                onClick={() => void send('deny')}
              >
                <Ban size={15} aria-hidden="true" />
                拒绝
              </button>
            )}
          </div>

          {state.context.actions.includes('reply') && (
            <div className="remote-action-reply">
              <label htmlFor="remote-action-reply">回复 Agent</label>
              <textarea
                id="remote-action-reply"
                aria-label="回复 Agent"
                value={reply}
                rows={3}
                maxLength={maxReplyBytes}
                placeholder="说明拒绝原因或下一步要求"
                onChange={(event) => {
                  setReply(event.currentTarget.value)
                  setReplyError(null)
                }}
              />
              <div className="remote-action-reply-footer">
                <span>{new TextEncoder().encode(reply).byteLength} / 4096 bytes</span>
                <button
                  type="button"
                  className="button button-secondary button-with-icon"
                  aria-label="发送回复"
                  onClick={sendReply}
                >
                  <MessageSquareText size={15} aria-hidden="true" />
                  发送回复
                </button>
              </div>
              {replyError !== null && (
                <p className="inline-error" role="alert">
                  {replyError}
                </p>
              )}
            </div>
          )}
          <p className="remote-action-expiry">
            授权有效至{' '}
            <time dateTime={state.context.expiresAt.iso}>
              {formatCapturedAt(state.context.expiresAt.epochMillis)}
            </time>
          </p>
        </>
      )}
    </section>
  )
}

export type ReplyValidation =
  | { readonly kind: 'valid'; readonly reply: string }
  | { readonly kind: 'invalid'; readonly message: string }

export function validateActionReply(value: string): ReplyValidation {
  const reply = value.trim()
  if (reply.length === 0) {
    return { kind: 'invalid', message: '请输入回复内容' }
  }
  if (controlCharacters.test(reply)) {
    return { kind: 'invalid', message: '回复不能包含控制字符或换行' }
  }
  if (new TextEncoder().encode(reply).byteLength > maxReplyBytes) {
    return { kind: 'invalid', message: '回复不能超过 4096 bytes' }
  }
  return { kind: 'valid', reply }
}

function actionProgressLabel(action: RemoteActionKind): string {
  switch (action) {
    case 'approve':
      return '批准'
    case 'deny':
      return '拒绝'
    case 'reply':
      return '发送回复'
    default: {
      const exhaustive: never = action
      return exhaustive
    }
  }
}

function formatCapturedAt(epochMillis: number): string {
  return new Date(epochMillis).toLocaleTimeString('zh-CN', {
    hour12: false,
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  })
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error)
}
