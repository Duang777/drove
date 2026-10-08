import { BellPlus, Send, Trash2 } from 'lucide-react'
import { useCallback, useEffect, useState } from 'react'
import {
  getNotificationStatus,
  listPushDevices,
  sendPushTest,
} from '../api/client'
import type { NotificationStatus, PushDevice } from '../api/types'
import {
  enableWebPush,
  loadPushDeviceState,
  needsIOSInstallGuidance,
  revokeCurrentWebPush,
  type PushDeviceState,
} from '../notifications/push'

type PanelState =
  | { readonly kind: 'loading' }
  | { readonly kind: 'failed'; readonly message: string }
  | {
      readonly kind: 'ready'
      readonly status: NotificationStatus
      readonly devices: ReadonlyArray<PushDevice>
      readonly push: PushDeviceState
    }

type ActionState =
  | { readonly kind: 'idle' }
  | { readonly kind: 'busy'; readonly action: 'enable' | 'test' | 'revoke' }
  | { readonly kind: 'message'; readonly tone: 'success' | 'error'; readonly text: string }

export function NotificationPanel() {
  const [state, setState] = useState<PanelState>({ kind: 'loading' })
  const [action, setAction] = useState<ActionState>({ kind: 'idle' })

  const refresh = useCallback(async () => {
    try {
      const [status, devices] = await Promise.all([
        getNotificationStatus(),
        listPushDevices(),
      ])
      const push = await loadPushDeviceState(status, devices)
      setState({ kind: 'ready', status, devices, push })
    } catch (error) {
      setState({
        kind: 'failed',
        message: error instanceof Error ? error.message : String(error),
      })
    }
  }, [])

  useEffect(() => {
    void refresh()
  }, [refresh])

  const currentDevice =
    state.kind === 'ready' && state.push.kind === 'enabled'
      ? state.push.device
      : null

  async function handleEnable(
    status: NotificationStatus,
  ): Promise<void> {
    if (status.webPush.kind !== 'available') return
    setAction({ kind: 'busy', action: 'enable' })
    try {
      await enableWebPush(status.webPush.vapidPublicKey)
      await refresh()
      setAction({
        kind: 'message',
        tone: 'success',
        text: '此设备已启用通知',
      })
    } catch (error) {
      setAction({
        kind: 'message',
        tone: 'error',
        text: error instanceof Error ? error.message : String(error),
      })
    }
  }

  async function handleTest(deviceID: string): Promise<void> {
    setAction({ kind: 'busy', action: 'test' })
    try {
      await sendPushTest(deviceID)
      setAction({
        kind: 'message',
        tone: 'success',
        text: '测试通知已发送',
      })
    } catch (error) {
      setAction({
        kind: 'message',
        tone: 'error',
        text: error instanceof Error ? error.message : String(error),
      })
    }
  }

  async function handleRevoke(deviceID: string): Promise<void> {
    setAction({ kind: 'busy', action: 'revoke' })
    try {
      await revokeCurrentWebPush(deviceID)
      await refresh()
      setAction({
        kind: 'message',
        tone: 'success',
        text: '此设备已停用通知',
      })
    } catch (error) {
      setAction({
        kind: 'message',
        tone: 'error',
        text: error instanceof Error ? error.message : String(error),
      })
    }
  }

  return (
    <section
      id="notification-settings"
      className="notification-panel"
      aria-labelledby="notification-title"
    >
      <div className="notification-heading">
        <div>
          <h2 id="notification-title">通知</h2>
          <p>Agent 进入 blocked 后发送，不包含终端内容或输入。</p>
        </div>
        {state.kind === 'ready' && (
          <span className="notification-device-count">
            {state.status.activeDeviceCount} 个设备
          </span>
        )}
      </div>

      {state.kind === 'loading' && (
        <p className="notification-loading" role="status">
          正在读取通知状态…
        </p>
      )}
      {state.kind === 'failed' && (
        <p className="inline-error notification-error" role="alert">
          无法读取通知状态：{state.message}
        </p>
      )}
      {state.kind === 'ready' && (
        <>
          <dl className="notification-facts">
            <div>
              <dt>浏览器</dt>
              <dd>{browserStateLabel(state.push)}</dd>
            </div>
            <div>
              <dt>权限</dt>
              <dd>{permissionLabel(state.push)}</dd>
            </div>
            <div>
              <dt>当前设备</dt>
              <dd>{currentDeviceLabel(state.push)}</dd>
            </div>
            <div>
              <dt>ntfy</dt>
              <dd>{state.status.ntfyAvailable ? '已配置' : '未配置'}</dd>
            </div>
          </dl>

          <p className="notification-policy">
            blocked 去抖 {state.status.policy.debounceSeconds} 秒
            {state.status.policy.quietWhenActive
              ? '，页面可见时静默'
              : '，页面可见时仍发送'}
          </p>
          {state.status.ntfyAvailable && (
            <p className="notification-note">
              Agent 名称、厂商、状态和时间会经过已配置的 ntfy 服务。
            </p>
          )}
          {needsIOSInstallGuidance() && (
            <p className="notification-note">
              iOS 需要先在 Safari 分享菜单中添加到主屏幕，再启用推送。
            </p>
          )}

          <div className="notification-actions">
            {state.push.kind === 'disabled' && (
              <button
                type="button"
                className="button button-primary button-with-icon"
                disabled={action.kind === 'busy'}
                onClick={() => void handleEnable(state.status)}
              >
                <BellPlus size={15} aria-hidden="true" />
                {action.kind === 'busy' && action.action === 'enable'
                  ? '启用中…'
                  : '启用此设备'}
              </button>
            )}
            {currentDevice !== null && (
              <>
                <button
                  type="button"
                  className="button button-secondary button-with-icon"
                  disabled={action.kind === 'busy'}
                  onClick={() => void handleTest(currentDevice.id)}
                >
                  <Send size={15} aria-hidden="true" />
                  {action.kind === 'busy' && action.action === 'test'
                    ? '发送中…'
                    : '发送测试'}
                </button>
                <button
                  type="button"
                  className="button button-danger button-with-icon"
                  disabled={action.kind === 'busy'}
                  onClick={() => void handleRevoke(currentDevice.id)}
                >
                  <Trash2 size={15} aria-hidden="true" />
                  {action.kind === 'busy' && action.action === 'revoke'
                    ? '停用中…'
                    : '停用此设备'}
                </button>
              </>
            )}
          </div>
          {action.kind === 'message' && (
            <p
              className={`notification-message notification-message-${action.tone}`}
              role={action.tone === 'error' ? 'alert' : 'status'}
            >
              {action.text}
            </p>
          )}
        </>
      )}
    </section>
  )
}

function browserStateLabel(state: PushDeviceState): string {
  switch (state.kind) {
    case 'unavailable':
      return state.reason
    case 'permission_denied':
      return '支持 Web Push'
    case 'disabled':
    case 'enabled':
      return '支持 Web Push'
    default: {
      const exhaustive: never = state
      return exhaustive
    }
  }
}

function permissionLabel(state: PushDeviceState): string {
  switch (state.kind) {
    case 'unavailable':
      return '不可用'
    case 'permission_denied':
      return '已阻止'
    case 'disabled':
      return state.permission === 'granted' ? '已允许' : '未请求'
    case 'enabled':
      return '已允许'
    default: {
      const exhaustive: never = state
      return exhaustive
    }
  }
}

function currentDeviceLabel(state: PushDeviceState): string {
  switch (state.kind) {
    case 'unavailable':
    case 'permission_denied':
    case 'disabled':
      return '未启用'
    case 'enabled':
      return state.device.deviceName
    default: {
      const exhaustive: never = state
      return exhaustive
    }
  }
}
