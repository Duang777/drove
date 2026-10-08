import { describe, expect, it } from 'vitest'
import type { PushDevice } from '../api/types'
import { derivePushDeviceState } from './push'

const device: PushDevice = {
  id: 'device-1',
  deviceName: 'macOS Chrome',
  createdAt: {
    iso: '2026-10-08T12:00:00Z',
    epochMillis: 1_791_460_800_000,
  },
}

describe('push device state', () => {
  it('requires daemon and browser support before enabling', () => {
    expect(
      derivePushDeviceState({
        daemonAvailable: false,
        unsupportedReason: null,
        permission: 'default',
        hasBrowserSubscription: false,
        storedDeviceID: null,
        devices: [],
      }),
    ).toEqual({
      kind: 'unavailable',
      reason: 'daemon 未启用 Web Push',
    })

    expect(
      derivePushDeviceState({
        daemonAvailable: true,
        unsupportedReason: '当前浏览器不支持 Web Push',
        permission: 'default',
        hasBrowserSubscription: false,
        storedDeviceID: null,
        devices: [],
      }),
    ).toEqual({
      kind: 'unavailable',
      reason: '当前浏览器不支持 Web Push',
    })
  })

  it('marks the current device enabled only when browser and daemon agree', () => {
    expect(
      derivePushDeviceState({
        daemonAvailable: true,
        unsupportedReason: null,
        permission: 'granted',
        hasBrowserSubscription: true,
        storedDeviceID: device.id,
        devices: [device],
      }),
    ).toEqual({ kind: 'enabled', device })

    expect(
      derivePushDeviceState({
        daemonAvailable: true,
        unsupportedReason: null,
        permission: 'granted',
        hasBrowserSubscription: false,
        storedDeviceID: device.id,
        devices: [device],
      }),
    ).toEqual({ kind: 'disabled', permission: 'granted' })
  })

  it('keeps denied permission distinct from an unsubscribed device', () => {
    expect(
      derivePushDeviceState({
        daemonAvailable: true,
        unsupportedReason: null,
        permission: 'denied',
        hasBrowserSubscription: false,
        storedDeviceID: null,
        devices: [],
      }),
    ).toEqual({ kind: 'permission_denied' })
  })
})
