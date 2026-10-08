import { describe, expect, it } from 'vitest'
import {
  parseNotificationStatus,
  parsePushDevices,
} from './notificationParsing'

describe('notification REST parsing', () => {
  it('parses public channel policy and device metadata', () => {
    expect(
      parseNotificationStatus({
        web_push: {
          available: true,
          vapid_public_key: 'public-vapid',
        },
        ntfy: { available: true },
        policy: {
          on: ['blocked'],
          debounce_seconds: 30,
          quiet_when_active: true,
        },
        active_device_count: 1,
      }),
    ).toEqual({
      webPush: {
        kind: 'available',
        vapidPublicKey: 'public-vapid',
      },
      ntfyAvailable: true,
      policy: {
        on: ['blocked'],
        debounceSeconds: 30,
        quietWhenActive: true,
      },
      activeDeviceCount: 1,
    })

    expect(
      parsePushDevices([
        {
          id: 'device-1',
          device_name: 'macOS Chrome',
          created_at: '2026-10-08T12:00:00Z',
        },
      ]),
    ).toMatchObject([
      {
        id: 'device-1',
        deviceName: 'macOS Chrome',
        createdAt: { iso: '2026-10-08T12:00:00Z' },
      },
    ])
  })

  it('rejects secret fields and contradictory availability', () => {
    expect(() =>
      parsePushDevices([
        {
          id: 'device-1',
          device_name: 'Phone',
          created_at: '2026-10-08T12:00:00Z',
          endpoint: 'https://push.example.test/secret',
        },
      ]),
    ).toThrow(/unexpected field endpoint/)

    expect(() =>
      parseNotificationStatus({
        web_push: {
          available: false,
          vapid_public_key: 'must-not-exist',
        },
        ntfy: { available: false },
        policy: {
          on: ['blocked'],
          debounce_seconds: 30,
          quiet_when_active: true,
        },
        active_device_count: 0,
      }),
    ).toThrow(/unexpected field vapid_public_key/)
  })

  it('rejects unsupported policy events and malformed counters', () => {
    expect(() =>
      parseNotificationStatus({
        web_push: { available: false },
        ntfy: { available: false },
        policy: {
          on: ['done'],
          debounce_seconds: -1,
          quiet_when_active: true,
        },
        active_device_count: -1,
      }),
    ).toThrow(/must contain blocked/)
  })
})
