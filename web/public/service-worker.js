const iconPath = '/icons/drove-192.png'
const actionLabels = {
  approve: '批准',
  deny: '拒绝',
  reply: '回复',
}

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
  const actionContext = readActionContext(payload)
  const actions =
    actionContext === null
      ? []
      : actionContext.tickets
          .filter((item) =>
            Object.prototype.hasOwnProperty.call(
              actionLabels,
              item.action,
            ),
          )
          .map((item) => ({
            action: item.action,
            title: actionLabels[item.action],
          }))
  const denyTicket = actionContext?.tickets.find(
    (item) => item.action === 'deny',
  )?.ticket
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
        actions,
        data: {
          deepLink: payload.deep_link,
          ...(actionContext === null
            ? {}
            : {
                agentID: payload.agent_id,
                blockedSeq: actionContext.blocked_seq,
              }),
          ...(denyTicket === undefined ? {} : { denyTicket }),
        },
      },
    ),
  )
})

self.addEventListener('notificationclick', (event) => {
  event.notification.close()
  const data = event.notification.data
  const target = approvalTarget(data)
  if (event.action === 'deny' && canDenyDirectly(data)) {
    event.waitUntil(denyOnce(data, target))
    return
  }
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
    typeof payload.agent_id !== 'string' ||
    typeof payload.agent_name !== 'string' ||
    typeof payload.vendor !== 'string' ||
    typeof payload.state !== 'string' ||
    typeof payload.deep_link !== 'string'
  ) {
    return null
  }
  return payload
}

function readActionContext(payload) {
  const value = payload.action_context
  if (
    typeof value !== 'object' ||
    value === null ||
    value.version !== 1 ||
    !isPositiveDecimal(value.blocked_seq) ||
    !Array.isArray(value.tickets) ||
    typeof value.expires_at !== 'string'
  ) {
    return null
  }
  const tickets = []
  const seen = new Set()
  for (const item of value.tickets) {
    if (
      typeof item !== 'object' ||
      item === null ||
      typeof item.action !== 'string' ||
      typeof item.ticket !== 'string' ||
      item.ticket.length === 0 ||
      seen.has(item.action)
    ) {
      continue
    }
    seen.add(item.action)
    tickets.push({ action: item.action, ticket: item.ticket })
  }
  if (tickets.length === 0) return null
  return { blocked_seq: value.blocked_seq, tickets }
}

function approvalTarget(data) {
  if (
    typeof data !== 'object' ||
    data === null ||
    typeof data.agentID !== 'string' ||
    data.agentID.length === 0 ||
    !isPositiveDecimal(data.blockedSeq)
  ) {
    return notificationTarget(data)
  }
  const target = new URL('/', self.location.origin)
  target.searchParams.set('agent', data.agentID)
  target.searchParams.set('blocked', data.blockedSeq)
  return target.href
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

function canDenyDirectly(data) {
  return (
    typeof data === 'object' &&
    data !== null &&
    typeof data.agentID === 'string' &&
    data.agentID.length > 0 &&
    isPositiveDecimal(data.blockedSeq) &&
    typeof data.denyTicket === 'string' &&
    data.denyTicket.length > 0
  )
}

async function denyOnce(data, fallbackTarget) {
  try {
    const response = await fetch(
      `/api/v1/agents/${encodeURIComponent(data.agentID)}/actions`,
      {
        method: 'POST',
        credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ ticket: data.denyTicket }),
      },
    )
    if (response.ok) return
  } catch {
    // Opening the page gives the operator a fresh, visible recovery path.
  }
  await openOrFocus(fallbackTarget)
}

function isPositiveDecimal(value) {
  if (
    typeof value !== 'string' ||
    !/^[1-9][0-9]*$/.test(value) ||
    value.length > 20
  ) {
    return false
  }
  try {
    return BigInt(value) <= 18_446_744_073_709_551_615n
  } catch {
    return false
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
