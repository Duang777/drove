import {
  requireArray,
  requireBoolean,
  requireKeys,
  requireNonNegativeInteger,
  requireRecord,
  requireString,
  requireTimestamp,
} from './parsing'
import type {
  NotificationStatus,
  PushDevice,
  WebPushAvailability,
} from './types'

export function parseNotificationStatus(value: unknown): NotificationStatus {
  const object = requireRecord(value, 'notification status')
  requireKeys(object, [
    'web_push',
    'ntfy',
    'policy',
    'active_device_count',
  ])
  return {
    webPush: parseWebPushAvailability(object.web_push),
    ntfyAvailable: parseChannelAvailability(object.ntfy, 'ntfy'),
    policy: parseNotificationPolicy(object.policy),
    activeDeviceCount: requireNonNegativeInteger(
      object.active_device_count,
      'notification status.active_device_count',
    ),
  }
}

export function parsePushDevices(value: unknown): PushDevice[] {
  return requireArray(value, 'push subscriptions').map((item, index) =>
    parsePushDeviceAt(item, `push subscriptions[${index}]`),
  )
}

export function parsePushDevice(value: unknown): PushDevice {
  return parsePushDeviceAt(value, 'push subscription')
}

function parsePushDeviceAt(value: unknown, name: string): PushDevice {
  const object = requireRecord(value, name)
  requireKeys(object, ['id', 'device_name', 'created_at'])
  return {
    id: requireString(object.id, `${name}.id`),
    deviceName: requireString(object.device_name, `${name}.device_name`),
    createdAt: requireTimestamp(object.created_at, `${name}.created_at`),
  }
}

function parseWebPushAvailability(value: unknown): WebPushAvailability {
  const object = requireRecord(value, 'notification status.web_push')
  const available = requireBoolean(
    object.available,
    'notification status.web_push.available',
  )
  if (!available) {
    requireKeys(object, ['available'])
    return { kind: 'unavailable' }
  }
  requireKeys(object, ['available', 'vapid_public_key'])
  return {
    kind: 'available',
    vapidPublicKey: requireString(
      object.vapid_public_key,
      'notification status.web_push.vapid_public_key',
    ),
  }
}

function parseChannelAvailability(value: unknown, name: string): boolean {
  const object = requireRecord(value, `notification status.${name}`)
  requireKeys(object, ['available'])
  return requireBoolean(
    object.available,
    `notification status.${name}.available`,
  )
}

function parseNotificationPolicy(
  value: unknown,
): NotificationStatus['policy'] {
  const object = requireRecord(value, 'notification status.policy')
  requireKeys(object, ['on', 'debounce_seconds', 'quiet_when_active'])
  const on = requireArray(
    object.on,
    'notification status.policy.on',
  ).map((item) => requireString(item, 'notification status.policy.on item'))
  if (on.length !== 1 || on[0] !== 'blocked') {
    throw new Error('notification status.policy.on must contain blocked')
  }
  return {
    on: ['blocked'],
    debounceSeconds: requireNonNegativeInteger(
      object.debounce_seconds,
      'notification status.policy.debounce_seconds',
    ),
    quietWhenActive: requireBoolean(
      object.quiet_when_active,
      'notification status.policy.quiet_when_active',
    ),
  }
}
