import { expect, test, type APIRequestContext, type Locator } from '@playwright/test'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const here = dirname(fileURLToPath(import.meta.url))
const approvalFixture = resolve(
  here,
  '../../internal/adapter/testdata/codex/approval.bin',
)

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
    await page.goto('/')
    await expect(page.getByText('已连接', { exact: true })).toBeVisible()

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

interface CreatedAgent {
  readonly agentID: string
  readonly name: string
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
