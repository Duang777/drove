const iconPath = '/icons/drove-192.png'

self.addEventListener('install', () => {
  self.skipWaiting()
})

self.addEventListener('activate', (event) => {
  event.waitUntil(self.clients.claim())
})

self.addEventListener('push', (event) => {
  const payload = readPayload(event.data)
  if (payload === null) return

  const isTest = payload.state === 'test'
  event.waitUntil(
    self.registration.showNotification(
      isTest ? 'Drove 通知已就绪' : `${payload.agent_name} 等待处理`,
      {
        body: isTest
          ? '此设备可以接收 Drove 通知。'
          : `${payload.vendor} · blocked`,
        icon: iconPath,
        badge: iconPath,
        tag: `drove-${payload.id}`,
        renotify: false,
        data: { deepLink: payload.deep_link },
      },
    ),
  )
})

self.addEventListener('notificationclick', (event) => {
  event.notification.close()
  const target = notificationTarget(event.notification.data)
  event.waitUntil(openOrFocus(target))
})

function readPayload(data) {
  if (data === null) return null
  let payload
  try {
    payload = data.json()
  } catch {
    return null
  }
  if (
    typeof payload !== 'object' ||
    payload === null ||
    typeof payload.id !== 'string' ||
    typeof payload.agent_name !== 'string' ||
    typeof payload.vendor !== 'string' ||
    typeof payload.state !== 'string' ||
    typeof payload.deep_link !== 'string'
  ) {
    return null
  }
  return payload
}

function notificationTarget(data) {
  const deepLink =
    typeof data === 'object' &&
    data !== null &&
    typeof data.deepLink === 'string'
      ? data.deepLink
      : '/'
  try {
    const target = new URL(deepLink, self.location.origin)
    if (target.origin !== self.location.origin || target.pathname !== '/') {
      return new URL('/', self.location.origin).href
    }
    return target.href
  } catch {
    return new URL('/', self.location.origin).href
  }
}

async function openOrFocus(target) {
  const windows = await self.clients.matchAll({
    type: 'window',
    includeUncontrolled: true,
  })
  const exact = windows.find((client) => client.url === target)
  if (exact !== undefined) {
    return focusWindow(exact)
  }
  const existing = windows.find(
    (client) => new URL(client.url).origin === self.location.origin,
  )
  if (existing !== undefined) {
    const navigated = await existing.navigate(target)
    return focusWindow(navigated ?? existing)
  }
  return self.clients.openWindow(target)
}

async function focusWindow(client) {
  try {
    return await client.focus()
  } catch {
    return client
  }
}
