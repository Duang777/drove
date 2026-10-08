import {
  recordNotificationPresence,
  revokePushSubscription,
  savePushSubscription,
} from '../api/client'
import type {
  NotificationStatus,
  PushDevice,
  PushSubscriptionInput,
} from '../api/types'

const serviceWorkerPath = '/service-worker.js'
const subscriptionIDKey = 'drove.push.subscription-id'
const presenceIntervalMillis = 20_000

export type PushDeviceState =
  | { readonly kind: 'unavailable'; readonly reason: string }
  | { readonly kind: 'permission_denied' }
  | {
      readonly kind: 'disabled'
      readonly permission: 'default' | 'granted'
    }
  | { readonly kind: 'enabled'; readonly device: PushDevice }

interface PushStateInput {
  readonly daemonAvailable: boolean
  readonly unsupportedReason: string | null
  readonly permission: NotificationPermission
  readonly hasBrowserSubscription: boolean
  readonly storedDeviceID: string | null
  readonly devices: ReadonlyArray<PushDevice>
}

export function derivePushDeviceState(input: PushStateInput): PushDeviceState {
  if (!input.daemonAvailable) {
    return {
      kind: 'unavailable',
      reason: 'daemon 未启用 Web Push',
    }
  }
  if (input.unsupportedReason !== null) {
    return { kind: 'unavailable', reason: input.unsupportedReason }
  }
  if (input.permission === 'denied') {
    return { kind: 'permission_denied' }
  }
  const device = input.devices.find(
    (candidate) => candidate.id === input.storedDeviceID,
  )
  if (input.hasBrowserSubscription && device !== undefined) {
    return { kind: 'enabled', device }
  }
  return { kind: 'disabled', permission: input.permission }
}

export async function loadPushDeviceState(
  status: NotificationStatus,
  devices: ReadonlyArray<PushDevice>,
): Promise<PushDeviceState> {
  const unsupportedReason = webPushUnsupportedReason()
  if (
    status.webPush.kind === 'unavailable' ||
    unsupportedReason !== null
  ) {
    return derivePushDeviceState({
      daemonAvailable: status.webPush.kind === 'available',
      unsupportedReason,
      permission: 'default',
      hasBrowserSubscription: false,
      storedDeviceID: null,
      devices,
    })
  }
  const registration = await registerDroveServiceWorker()
  const subscription = await registration.pushManager.getSubscription()
  return derivePushDeviceState({
    daemonAvailable: true,
    unsupportedReason: null,
    permission: Notification.permission,
    hasBrowserSubscription: subscription !== null,
    storedDeviceID: window.localStorage.getItem(subscriptionIDKey),
    devices,
  })
}

export async function registerDroveServiceWorker(): Promise<ServiceWorkerRegistration> {
  const unsupportedReason = webPushUnsupportedReason()
  if (unsupportedReason !== null) {
    throw new Error(unsupportedReason)
  }
  await navigator.serviceWorker.register(serviceWorkerPath, { scope: '/' })
  return navigator.serviceWorker.ready
}

export async function enableWebPush(
  vapidPublicKey: string,
): Promise<PushDevice> {
  const registration = await registerDroveServiceWorker()
  const permission = await Notification.requestPermission()
  if (permission !== 'granted') {
    throw new Error(
      permission === 'denied'
        ? '浏览器已阻止通知，请在站点设置中重新授权'
        : '未授予通知权限',
    )
  }
  const existing = await registration.pushManager.getSubscription()
  const subscription =
    existing ??
    (await registration.pushManager.subscribe({
      userVisibleOnly: true,
      applicationServerKey: decodeBase64URL(vapidPublicKey),
    }))
  const input = pushSubscriptionInput(subscription)
  const device = await savePushSubscription(input)
  window.localStorage.setItem(subscriptionIDKey, device.id)
  return device
}

export async function revokeCurrentWebPush(deviceID: string): Promise<void> {
  await revokePushSubscription(deviceID)
  const registration = await registerDroveServiceWorker()
  const subscription = await registration.pushManager.getSubscription()
  if (subscription !== null) {
    await subscription.unsubscribe()
  }
  window.localStorage.removeItem(subscriptionIDKey)
}

export function startPresenceHeartbeat(): () => void {
  let timer: number | undefined
  const send = () => {
    if (document.visibilityState !== 'visible') return
    void recordNotificationPresence().catch(() => {})
  }
  const stopTimer = () => {
    if (timer === undefined) return
    window.clearInterval(timer)
    timer = undefined
  }
  const startTimer = () => {
    stopTimer()
    if (document.visibilityState !== 'visible') return
    send()
    timer = window.setInterval(send, presenceIntervalMillis)
  }
  const handleVisibility = () => {
    startTimer()
  }
  document.addEventListener('visibilitychange', handleVisibility)
  startTimer()
  return () => {
    stopTimer()
    document.removeEventListener('visibilitychange', handleVisibility)
  }
}

export function needsIOSInstallGuidance(): boolean {
  const isIOS = /iPad|iPhone|iPod/.test(navigator.userAgent)
  return (
    isIOS &&
    !window.matchMedia('(display-mode: standalone)').matches
  )
}

function webPushUnsupportedReason(): string | null {
  if (!window.isSecureContext) {
    return 'Web Push 需要 HTTPS 或本机地址'
  }
  if (
    !('serviceWorker' in navigator) ||
    !('PushManager' in window) ||
    !('Notification' in window)
  ) {
    return '当前浏览器不支持 Web Push'
  }
  return null
}

function pushSubscriptionInput(
  subscription: PushSubscription,
): PushSubscriptionInput {
  const p256dh = subscription.getKey('p256dh')
  const auth = subscription.getKey('auth')
  if (p256dh === null || auth === null) {
    throw new Error('浏览器返回的推送订阅缺少密钥')
  }
  return {
    endpoint: subscription.endpoint,
    p256dh: encodeBase64URL(p256dh),
    auth: encodeBase64URL(auth),
    deviceName: browserDeviceName(),
  }
}

function browserDeviceName(): string {
  const agent = navigator.userAgent
  const browser = agent.includes('Firefox')
    ? 'Firefox'
    : agent.includes('Edg/')
      ? 'Edge'
      : agent.includes('Chrome/')
        ? 'Chrome'
        : agent.includes('Safari/')
          ? 'Safari'
          : 'Browser'
  const device = /iPad|iPhone|iPod/.test(agent)
    ? 'iOS'
    : agent.includes('Android')
      ? 'Android'
      : agent.includes('Mac OS X')
        ? 'macOS'
        : agent.includes('Windows')
          ? 'Windows'
          : agent.includes('Linux')
            ? 'Linux'
            : 'Device'
  return `${device} ${browser}`
}

function decodeBase64URL(value: string): ArrayBuffer {
  const padded = value.replace(/-/g, '+').replace(/_/g, '/').padEnd(
    Math.ceil(value.length / 4) * 4,
    '=',
  )
  const binary = window.atob(padded)
  const buffer = new ArrayBuffer(binary.length)
  const bytes = new Uint8Array(buffer)
  for (let index = 0; index < binary.length; index++) {
    bytes[index] = binary.charCodeAt(index)
  }
  return buffer
}

function encodeBase64URL(value: ArrayBuffer): string {
  const bytes = new Uint8Array(value)
  let binary = ''
  for (const byte of bytes) {
    binary += String.fromCharCode(byte)
  }
  return window
    .btoa(binary)
    .replace(/\+/g, '-')
    .replace(/\//g, '_')
    .replace(/=+$/, '')
}
