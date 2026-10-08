import { expect, test, type APIRequestContext, type Locator } from '@playwright/test'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const here = dirname(fileURLToPath(import.meta.url))
const approvalFixture = resolve(
  here,
  '../../internal/adapter/testdata/codex/approval.bin',
)
const pushPublicKey =
  'BGbO2S2tz3pivutjJWd-uU9CDTjHxbKlumyLfljDKhIJxM1g0SPtrGYzk-JcQ7vcrPUTpVKEYoTm1FUWGcbxBq0'
const pushAuth = '-XrP1hCEVz_XgHLJenwHwg'

test('operator can triage and answer a blocked agent from the fleet', async ({
  page,
  request,
}, testInfo) => {
  const createdAgentIDs: string[] = []
  try {
    const runSuffix =
      testInfo.repeatEachIndex === 0 && testInfo.retry === 0
        ? ''
        : `-${testInfo.repeatEachIndex}-${testInfo.retry}`
    const presenceResponsePromise = page.waitForResponse(
      (response) =>
        response.request().method() === 'POST' &&
        new URL(response.url()).pathname ===
          '/api/v1/notifications/presence',
    )
    await page.goto('/')
    expect((await presenceResponsePromise).status()).toBe(204)
    await expect(page.getByText('已连接', { exact: true })).toBeVisible()
    await expect
      .poll(() =>
        page.evaluate(async () => {
          const registration = await navigator.serviceWorker.ready
          return registration.active?.scriptURL.endsWith('/service-worker.js')
        }),
      )
      .toBe(true)

    await page.getByLabel('Agent vendor').selectOption('generic')
    await page.getByLabel('Generic command').fill('/bin/cat')
    const genericResponsePromise = page.waitForResponse(
      (response) =>
        response.request().method() === 'POST' &&
        new URL(response.url()).pathname === '/api/v1/agents',
    )
    await page.getByRole('button', { name: '启动 Agent' }).click()
    const genericResponse = await genericResponsePromise
    expect(genericResponse.status()).toBe(201)
    const genericAgent = parseAgent(await genericResponse.json())
    createdAgentIDs.push(genericAgent.agentID)

    const concurrentAgents = [
      await createWaitingAgent(
        request,
        'claude',
        `claude-agent-1${runSuffix}`,
      ),
      await createWaitingAgent(
        request,
        'claude',
        `claude-agent-2${runSuffix}`,
      ),
      await createWaitingAgent(request, 'codex', `codex-agent-1${runSuffix}`),
      await createStagedBlockedAgent(request, `blocked-agent${runSuffix}`),
    ]
    createdAgentIDs.push(...concurrentAgents.map((agent) => agent.agentID))
    const blockedAgent = concurrentAgents[3]
    if (blockedAgent === undefined) {
      throw new Error('expected staged blocked agent')
    }

    const blockedCard = agentCard(page.locator('.agent-grid'), blockedAgent.name)
    const genericCard = agentCard(
      page.locator('.agent-grid'),
      genericAgent.agentID.slice(0, 8),
    )
    await expect(page.locator('.agent-card .status-working')).toHaveCount(5)
    for (const agent of concurrentAgents) {
      await expect(
        agentCard(page.locator('.agent-grid'), agent.name),
      ).toBeVisible()
    }
    await expect(blockedCard).not.toHaveClass(/agent-card-blocked/)

    const blockedLatencyMillis = await triggerBlockedAndMeasure(
      blockedCard,
      blockedAgent.agentID,
    )
    expect(blockedLatencyMillis).toBeLessThanOrEqual(1_000)
    await expect(blockedCard).toHaveClass(/agent-card-blocked/)
    await expect(blockedCard.getByText('blocked', { exact: true })).toBeVisible()
    await expect(blockedCard.locator('.agent-terminal-link pre')).toContainText(
      'Press enter to confirm',
    )
    await expect(blockedCard.locator('.agent-card-context')).toContainText(
      'screen / codex.approval_prompt',
    )
    await expect(genericCard).toBeVisible()
    await expect(page.locator('.agent-card').first()).toContainText(
      blockedAgent.name,
    )

    const animationName = await blockedCard.evaluate((element) =>
      getComputedStyle(element, '::after').animationName,
    )
    expect(animationName).toBe('blocked-pulse')

    await expect
      .poll(async () => blockedSeconds(await blockedWaitText(blockedCard)))
      .toBeGreaterThanOrEqual(1)
    const beforeReload = blockedSeconds(await blockedWaitText(blockedCard))
    await page.reload()
    await expect(blockedCard).toHaveClass(/agent-card-blocked/)
    const afterReload = blockedSeconds(await blockedWaitText(blockedCard))
    expect(afterReload).toBeGreaterThanOrEqual(beforeReload)

    await page.screenshot({
      path: testInfo.outputPath('fleet-desktop.png'),
      fullPage: true,
    })

    await page.keyboard.press('1')
    await expect(page).toHaveURL(
      new RegExp(`[?&]agent=${blockedAgent.agentID}(?:&|$)`),
    )
    await expect(page.getByText('可输入', { exact: true })).toBeVisible()
    const terminalInput = page.locator('textarea.xterm-helper-textarea')
    await expect(terminalInput).toBeFocused()
    await page.keyboard.type('approved')
    await page.keyboard.press('Enter')
    await expect(page.getByText('working', { exact: true })).toBeVisible()

    await page.keyboard.press('Escape')
    await expect(page).toHaveURL(/\/$/)
    await expect(page.getByRole('heading', { name: '会话' })).toBeVisible()
    await expect(blockedCard).not.toHaveClass(/agent-card-blocked/)
    await expect(blockedCard.getByText('working', { exact: true })).toBeVisible()
    await expect(blockedCard.locator('.agent-terminal-link pre')).toContainText(
      'Would you like to run the following command?',
    )
    const inputEvent = page
      .locator('.event-row')
      .filter({ hasText: '[agent.input]' })
      .filter({ hasText: 'bytes' })
    await expect(inputEvent.first()).toBeVisible()

    await page.setViewportSize({ width: 375, height: 812 })
    await expectNoHorizontalOverflow(page.locator('html'))
    await page.screenshot({
      path: testInfo.outputPath('fleet-mobile-375.png'),
      fullPage: true,
    })

    await page.setViewportSize({ width: 320, height: 720 })
    await expectNoHorizontalOverflow(page.locator('html'))
    await page.screenshot({
      path: testInfo.outputPath('fleet-mobile-320.png'),
      fullPage: true,
    })
  } finally {
    await stopAgents(request, createdAgentIDs)
  }
})

test('operator can enable, test, and revoke Web Push', async ({
  page,
}, testInfo) => {
  await page.addInitScript(
    ({ auth, publicKey }) => {
      let notificationPermission: NotificationPermission = 'default'
      let currentSubscription: {
        endpoint: string
        getKey: (name: string) => ArrayBuffer | null
        unsubscribe: () => Promise<boolean>
      } | null = null
      const decode = (value: string): ArrayBuffer => {
        const padded = value
          .replace(/-/g, '+')
          .replace(/_/g, '/')
          .padEnd(Math.ceil(value.length / 4) * 4, '=')
        const binary = window.atob(padded)
        const buffer = new ArrayBuffer(binary.length)
        const bytes = new Uint8Array(buffer)
        for (let index = 0; index < binary.length; index++) {
          bytes[index] = binary.charCodeAt(index)
        }
        return buffer
      }
      const registration = {
        pushManager: {
          getSubscription: async () => currentSubscription,
          subscribe: async () => {
            currentSubscription = {
              endpoint: 'https://push.example.test/e2e-device',
              getKey: (name: string) => {
                if (name === 'p256dh') return decode(publicKey)
                if (name === 'auth') return decode(auth)
                return null
              },
              unsubscribe: async () => {
                currentSubscription = null
                return true
              },
            }
            return currentSubscription
          },
        },
      }
      Object.defineProperty(navigator, 'serviceWorker', {
        configurable: true,
        value: {
          register: async () => registration,
          ready: Promise.resolve(registration),
        },
      })
      Object.defineProperty(window, 'PushManager', {
        configurable: true,
        value: function PushManager() {},
      })
      Object.defineProperty(window.Notification, 'permission', {
        configurable: true,
        get: () => notificationPermission,
      })
      Object.defineProperty(window.Notification, 'requestPermission', {
        configurable: true,
        value: async () => {
          notificationPermission = 'granted'
          return notificationPermission
        },
      })
    },
    { auth: pushAuth, publicKey: pushPublicKey },
  )
  await page.route('**/api/v1/notifications/test', async (route) => {
    expect(route.request().method()).toBe('POST')
    await route.fulfill({ status: 204 })
  })

  await page.goto('/')
  await page.getByRole('button', { name: '打开通知设置' }).click()
  await expect(
    page.getByRole('heading', { name: '通知', exact: true }),
  ).toBeVisible()
  await expect(page.getByText('未请求', { exact: true })).toBeVisible()

  await page.getByRole('button', { name: '启用此设备' }).click()
  await expect(page.getByText('此设备已启用通知')).toBeVisible()
  const currentDevice = page
    .locator('.notification-facts > div')
    .filter({ hasText: '当前设备' })
    .locator('dd')
  await expect(currentDevice).toHaveText(/ Chrome$/)

  await page.getByRole('button', { name: '发送测试' }).click()
  await expect(page.getByText('测试通知已发送')).toBeVisible()
  await page.screenshot({
    path: testInfo.outputPath('notifications-desktop.png'),
    fullPage: true,
  })

  await page.setViewportSize({ width: 375, height: 812 })
  await expectNoHorizontalOverflow(page.locator('html'))
  await page.screenshot({
    path: testInfo.outputPath('notifications-mobile-375.png'),
    fullPage: true,
  })

  await page.setViewportSize({ width: 320, height: 720 })
  await expectNoHorizontalOverflow(page.locator('html'))
  await page.screenshot({
    path: testInfo.outputPath('notifications-mobile-320.png'),
    fullPage: true,
  })

  await page.getByRole('button', { name: '停用此设备' }).click()
  await expect(page.getByText('此设备已停用通知')).toBeVisible()
  await expect(
    page.getByRole('button', { name: '启用此设备' }),
  ).toBeVisible()
})

test('non-denial notification actions open the approval page without tickets in the URL', async ({
  context,
  page,
}) => {
  const workerPromise = context.waitForEvent('serviceworker')
  await page.goto('/')
  const worker = await workerPromise
  for (const action of ['approve', 'reply', '', 'launch']) {
    const agentID = `push-agent-${action || 'empty'}`
    await worker.evaluate(
      async ({ clickedAction, targetAgent }) => {
        let completion: Promise<unknown> | undefined
        const event = new Event('notificationclick')
        Object.defineProperty(event, 'action', { value: clickedAction })
        Object.defineProperty(event, 'notification', {
          value: {
            close() {},
            data: {
              agentID: targetAgent,
              blockedSeq: '9007199254740993',
              deepLink: `/?agent=${encodeURIComponent(targetAgent)}`,
            },
          },
        })
        Object.defineProperty(event, 'waitUntil', {
          value: (promise: Promise<unknown>) => {
            completion = promise
          },
        })
        self.dispatchEvent(event)
        await completion
      },
      { clickedAction: action, targetAgent: agentID },
    )
    await expect(page).toHaveURL(
      new RegExp(
        `[?&]agent=${agentID}&blocked=9007199254740993(?:&|$)`,
      ),
    )
    expect(page.url()).not.toContain('ticket')
  }
})

test('service worker exposes only declared supported notification actions', async ({
  context,
  page,
}) => {
  const workerPromise = context.waitForEvent('serviceworker')
  await page.goto('/')
  const worker = await workerPromise
  const shown = await worker.evaluate(async () => {
    let completion: Promise<unknown> | undefined
    let captured:
      | {
          readonly title: string
          readonly actions: ReadonlyArray<{
            readonly action: string
            readonly title: string
          }>
          readonly data: unknown
        }
      | undefined
    const registration = self.registration
    const original = registration.showNotification.bind(registration)
    Object.defineProperty(registration, 'showNotification', {
      configurable: true,
      value: async (title: string, options?: NotificationOptions) => {
        captured = {
          title,
          actions: options?.actions ?? [],
          data: options?.data,
        }
      },
    })
    try {
      const event = new Event('push')
      Object.defineProperty(event, 'data', {
        value: {
          json: () => ({
            id: 'delivery-1',
            agent_id: 'agent-1',
            agent_name: 'Reviewer',
            vendor: 'codex',
            state: 'blocked',
            blocked_seq: 9_007_199_254_740_992,
            deep_link: '/?agent=agent-1',
            action_context: {
              version: 1,
              blocked_seq: '9007199254740993',
              tickets: [
                { action: 'approve', ticket: 'approve-ticket' },
                { action: 'deny', ticket: 'deny-ticket' },
                { action: 'toString', ticket: 'unsupported-ticket' },
              ],
              expires_at: '2026-10-08T18:10:00Z',
            },
          }),
        },
      })
      Object.defineProperty(event, 'waitUntil', {
        value: (promise: Promise<unknown>) => {
          completion = promise
        },
      })
      self.dispatchEvent(event)
      await completion
      return captured
    } finally {
      Object.defineProperty(registration, 'showNotification', {
        configurable: true,
        value: original,
      })
    }
  })

  expect(shown).toMatchObject({
    title: 'Reviewer 等待处理',
    actions: [
      { action: 'approve', title: '批准' },
      { action: 'deny', title: '拒绝' },
    ],
    data: {
      agentID: 'agent-1',
      blockedSeq: '9007199254740993',
      denyTicket: 'deny-ticket',
    },
  })
})

test('failed direct denial posts once and opens the approval page', async ({
  context,
  page,
}) => {
  const workerPromise = context.waitForEvent('serviceworker')
  await page.goto('/')
  const worker = await workerPromise
  const requests = await worker.evaluate(async () => {
    let completion: Promise<unknown> | undefined
    const calls: Array<{ url: string; init?: RequestInit }> = []
    const original = self.fetch
    Object.defineProperty(self, 'fetch', {
      configurable: true,
      value: async (url: string, init?: RequestInit) => {
        calls.push({ url, init })
        return new Response('', { status: 409 })
      },
    })
    try {
      const event = new Event('notificationclick')
      Object.defineProperty(event, 'action', { value: 'deny' })
      Object.defineProperty(event, 'notification', {
        value: {
          close() {},
          data: {
            agentID: 'agent/one',
            blockedSeq: '9007199254740993',
            deepLink: '/?agent=agent%2Fone',
            denyTicket: 'deny-ticket',
          },
        },
      })
      Object.defineProperty(event, 'waitUntil', {
        value: (promise: Promise<unknown>) => {
          completion = promise
        },
      })
      self.dispatchEvent(event)
      await completion
      return calls
    } finally {
      Object.defineProperty(self, 'fetch', {
        configurable: true,
        value: original,
      })
    }
  })

  expect(requests).toHaveLength(1)
  expect(requests[0]).toMatchObject({
    url: '/api/v1/agents/agent%2Fone/actions',
    init: {
      method: 'POST',
      credentials: 'same-origin',
      body: JSON.stringify({ ticket: 'deny-ticket' }),
    },
  })
  await expect(page).toHaveURL(
    /[?&]agent=agent%2Fone&blocked=9007199254740993(?:&|$)/,
  )
})

test('remote approval page executes approve, reply, and direct denial against the daemon', async ({
  context,
  page,
  request,
}, testInfo) => {
  const createdAgentIDs: string[] = []
  try {
    const deviceID = await registerPushDevice(request, testInfo.testId)
    await page.addInitScript((id) => {
      window.localStorage.setItem('drove.push.subscription-id', id)
    }, deviceID)

    const approveAgent = await createBlockedAgent(
      request,
      `approve-agent-${testInfo.retry}`,
    )
    createdAgentIDs.push(approveAgent.agentID)
    await page.goto(actionPageURL(approveAgent))
    await expect(
      page.getByRole('heading', { name: '等待你的决定' }),
    ).toBeVisible()
    await expect(page.locator('.remote-action-screen pre')).toContainText(
      'Press enter to confirm',
    )
    await expectNoHorizontalOverflow(page.locator('html'))
    await page.screenshot({
      path: testInfo.outputPath('approval-desktop.png'),
      fullPage: true,
    })
    await page.setViewportSize({ width: 375, height: 812 })
    await expectNoHorizontalOverflow(page.locator('html'))
    await page.screenshot({
      path: testInfo.outputPath('approval-mobile-375.png'),
      fullPage: true,
    })
    await page.setViewportSize({ width: 320, height: 720 })
    await expectNoHorizontalOverflow(page.locator('html'))
    await page.screenshot({
      path: testInfo.outputPath('approval-mobile-320.png'),
      fullPage: true,
    })
    await page.setViewportSize({ width: 1280, height: 900 })

    await page.getByRole('button', { name: '批准请求' }).click()
    await expect(page.getByText('确认批准当前请求？')).toBeVisible()
    const approveResponse = page.waitForResponse(
      (response) =>
        response.request().method() === 'POST' &&
        new URL(response.url()).pathname ===
          `/api/v1/agents/${approveAgent.agentID}/actions`,
    )
    await page.getByRole('button', { name: '确认批准请求' }).click()
    expect((await approveResponse).status()).toBe(200)
    await expectActionAudit(request, approveAgent.agentID, 'approve', 0)

    const replyAgent = await createBlockedAgent(
      request,
      `reply-agent-${testInfo.retry}`,
    )
    createdAgentIDs.push(replyAgent.agentID)
    await page.goto(actionPageURL(replyAgent))
    await expect(page.getByLabel('回复 Agent')).toBeVisible()
    await page.getByLabel('回复 Agent').fill('continue read only')
    const replyResponse = page.waitForResponse(
      (response) =>
        response.request().method() === 'POST' &&
        new URL(response.url()).pathname ===
          `/api/v1/agents/${replyAgent.agentID}/actions`,
    )
    await page.getByRole('button', { name: '发送回复' }).click()
    expect((await replyResponse).status()).toBe(200)
    await expectActionAudit(
      request,
      replyAgent.agentID,
      'reply',
      new TextEncoder().encode('continue read only').byteLength,
    )

    const denyAgent = await createBlockedAgent(
      request,
      `deny-agent-${testInfo.retry}`,
    )
    createdAgentIDs.push(denyAgent.agentID)
    const actionContext = await request.post(
      `/api/v1/agents/${encodeURIComponent(denyAgent.agentID)}/action-context`,
      {
        data: {
          blocked_seq: denyAgent.blockedSeq,
          device_id: deviceID,
        },
      },
    )
    expect(actionContext.status()).toBe(200)
    const denyTicket = actionTicket(await actionContext.json(), 'deny')
    const worker =
      context.serviceWorkers()[0] ??
      (await context.waitForEvent('serviceworker'))
    const beforeDirectDeny = page.url()
    await worker.evaluate(
      async ({ agentID, blockedSeq, ticket }) => {
        let completion: Promise<unknown> | undefined
        const event = new Event('notificationclick')
        Object.defineProperty(event, 'action', { value: 'deny' })
        Object.defineProperty(event, 'notification', {
          value: {
            close() {},
            data: {
              agentID,
              blockedSeq,
              deepLink: `/?agent=${encodeURIComponent(agentID)}`,
              denyTicket: ticket,
            },
          },
        })
        Object.defineProperty(event, 'waitUntil', {
          value: (promise: Promise<unknown>) => {
            completion = promise
          },
        })
        self.dispatchEvent(event)
        await completion
      },
      {
        agentID: denyAgent.agentID,
        blockedSeq: denyAgent.blockedSeq,
        ticket: denyTicket,
      },
    )
    expect(page.url()).toBe(beforeDirectDeny)
    await expectActionAudit(request, denyAgent.agentID, 'deny', 0)
  } finally {
    await stopAgents(request, createdAgentIDs)
  }
})

interface CreatedAgent {
  readonly agentID: string
  readonly name: string
}

interface BlockedAgent extends CreatedAgent {
  readonly blockedSeq: string
}

type Vendor = 'claude' | 'codex'

async function createWaitingAgent(
  request: APIRequestContext,
  vendor: Vendor,
  name: string,
): Promise<CreatedAgent> {
  const response = await request.post('/api/v1/agents', {
    data: {
      vendor,
      name,
      command: '/bin/cat',
      hooks: 'off',
    },
  })
  expect(response.status()).toBe(201)
  return parseAgent(await response.json())
}

async function createStagedBlockedAgent(
  request: APIRequestContext,
  name: string,
): Promise<CreatedAgent> {
  const response = await request.post('/api/v1/agents', {
    data: {
      vendor: 'codex',
      name,
      command: '/bin/sh',
      args: [
        '-c',
        'read trigger; cat "$1"; printf "\\033[20T"; exec /bin/cat',
        'drove-e2e',
        approvalFixture,
      ],
      hooks: 'off',
    },
  })
  expect(response.status()).toBe(201)
  return parseAgent(await response.json())
}

async function createBlockedAgent(
  request: APIRequestContext,
  name: string,
): Promise<BlockedAgent> {
  const created = await createStagedBlockedAgent(request, name)
  const input = await request.post(
    `/api/v1/agents/${encodeURIComponent(created.agentID)}/input`,
    { data: { data: 'go\n' } },
  )
  expect(input.status()).toBe(204)

  let blockedSeq = ''
  await expect
    .poll(async () => {
      const response = await request.get('/api/v1/agents')
      expect(response.status()).toBe(200)
      const value: unknown = await response.json()
      if (!Array.isArray(value)) return ''
      const current = value.find(
        (item) =>
          typeof item === 'object' &&
          item !== null &&
          'agent_id' in item &&
          item.agent_id === created.agentID,
      )
      if (
        typeof current !== 'object' ||
        current === null ||
        !('state' in current) ||
        current.state !== 'blocked' ||
        !('state_seq' in current) ||
        typeof current.state_seq !== 'string'
      ) {
        return ''
      }
      blockedSeq = current.state_seq
      return blockedSeq
    })
    .toMatch(/^[1-9][0-9]*$/)
  return { ...created, blockedSeq }
}

async function registerPushDevice(
  request: APIRequestContext,
  suffix: string,
): Promise<string> {
  const response = await request.post('/api/v1/push/subscriptions', {
    data: {
      endpoint: `https://push.example.test/${encodeURIComponent(suffix)}`,
      keys: {
        p256dh: pushPublicKey,
        auth: pushAuth,
      },
      device_name: 'E2E browser',
    },
  })
  expect(response.status()).toBe(201)
  const value: unknown = await response.json()
  if (
    typeof value !== 'object' ||
    value === null ||
    !('id' in value) ||
    typeof value.id !== 'string'
  ) {
    throw new Error('push subscription response must contain id')
  }
  return value.id
}

function actionPageURL(agent: BlockedAgent): string {
  const query = new URLSearchParams({
    agent: agent.agentID,
    blocked: agent.blockedSeq,
  })
  return `/?${query.toString()}`
}

function actionTicket(value: unknown, action: string): string {
  if (
    typeof value !== 'object' ||
    value === null ||
    !('tickets' in value) ||
    !Array.isArray(value.tickets)
  ) {
    throw new Error('action context must contain tickets')
  }
  const match = value.tickets.find(
    (item) =>
      typeof item === 'object' &&
      item !== null &&
      'action' in item &&
      item.action === action,
  )
  if (
    typeof match !== 'object' ||
    match === null ||
    !('ticket' in match) ||
    typeof match.ticket !== 'string'
  ) {
    throw new Error(`action context has no ${action} ticket`)
  }
  return match.ticket
}

async function expectActionAudit(
  request: APIRequestContext,
  agentID: string,
  action: string,
  replyBytes: number,
): Promise<void> {
  await expect
    .poll(async () => {
      const response = await request.get(
        `/api/v1/agents/${encodeURIComponent(agentID)}/events`,
      )
      if (!response.ok()) return null
      const value: unknown = await response.json()
      if (!Array.isArray(value)) return null
      for (const row of value) {
        if (
          typeof row !== 'object' ||
          row === null ||
          !('Type' in row) ||
          row.Type !== 'agent.action' ||
          !('Payload' in row) ||
          typeof row.Payload !== 'string'
        ) {
          continue
        }
        const payload: unknown = JSON.parse(row.Payload)
        if (
          typeof payload === 'object' &&
          payload !== null &&
          'action' in payload &&
          payload.action === action &&
          'reply_bytes' in payload &&
          payload.reply_bytes === replyBytes
        ) {
          return { action, replyBytes }
        }
      }
      return null
    })
    .toEqual({ action, replyBytes })
}

async function triggerBlockedAndMeasure(
  card: Locator,
  agentID: string,
): Promise<number> {
  return card.evaluate(async (element, id) => {
    return new Promise<number>((resolveLatency, rejectLatency) => {
      let timeoutID = 0
      let screenSignalAt: number | null = null
      const observer = new MutationObserver(() => {
        if (screenSignalAt === null) {
          const signalRendered = [...document.querySelectorAll('.event-row')]
            .some((row) => {
              const text = row.textContent ?? ''
              return (
                text.includes('screen') &&
                text.includes('human_input_required') &&
                text.includes('candidate')
              )
            })
          if (signalRendered) screenSignalAt = performance.now()
        }
        if (
          screenSignalAt === null ||
          !element.classList.contains('agent-card-blocked')
        ) {
          return
        }
        observer.disconnect()
        window.clearTimeout(timeoutID)
        resolveLatency(performance.now() - screenSignalAt)
      })
      observer.observe(document.body, {
        attributes: true,
        childList: true,
        subtree: true,
      })
      timeoutID = window.setTimeout(() => {
        observer.disconnect()
        rejectLatency(new Error('blocked state did not render after input'))
      }, 3_000)
      void fetch(`/api/v1/agents/${encodeURIComponent(id)}/input`, {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({ data: 'go\n' }),
      })
        .then(async (response) => {
          if (response.ok) return
          throw new Error(
            `input request failed: ${response.status} ${await response.text()}`,
          )
        })
        .catch((error: unknown) => {
          observer.disconnect()
          window.clearTimeout(timeoutID)
          rejectLatency(
            error instanceof Error ? error : new Error(String(error)),
          )
        })
    })
  }, agentID)
}

function parseAgent(value: unknown): CreatedAgent {
  if (typeof value !== 'object' || value === null) {
    throw new Error('agent response must be an object')
  }
  if (!('agent_id' in value) || typeof value.agent_id !== 'string') {
    throw new Error('agent response must contain agent_id')
  }
  if (!('name' in value) || typeof value.name !== 'string') {
    throw new Error('agent response must contain name')
  }
  return { agentID: value.agent_id, name: value.name }
}

function agentCard(grid: Locator, text: string): Locator {
  return grid.locator('.agent-card').filter({ hasText: text })
}

async function blockedWaitText(card: Locator): Promise<string> {
  const text = await card.locator('.agent-blocked-wait').textContent()
  if (text === null) throw new Error('blocked timer is missing')
  return text
}

function blockedSeconds(text: string): number {
  const match = /已等你 (?:(\d+) 小时 )?(?:(\d+) 分 )?(\d+) 秒/.exec(text)
  if (match === null) throw new Error(`invalid blocked timer: ${text}`)
  const hours = Number(match[1] ?? 0)
  const minutes = Number(match[2] ?? 0)
  const seconds = Number(match[3])
  return hours * 3600 + minutes * 60 + seconds
}

async function expectNoHorizontalOverflow(html: Locator): Promise<void> {
  const dimensions = await html.evaluate((element) => ({
    clientWidth: element.clientWidth,
    scrollWidth: element.scrollWidth,
  }))
  expect(dimensions.scrollWidth).toBeLessThanOrEqual(dimensions.clientWidth)
}

async function stopAgents(
  request: APIRequestContext,
  agentIDs: ReadonlyArray<string>,
): Promise<void> {
  for (const agentID of agentIDs) {
    const response = await request.delete(
      `/api/v1/agents/${encodeURIComponent(agentID)}`,
    )
    if (response.status() !== 204 && response.status() !== 404) {
      throw new Error(`failed to stop ${agentID}: ${response.status()}`)
    }
  }
}
