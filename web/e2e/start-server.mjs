import { spawn } from 'node:child_process'
import { once } from 'node:events'
import { mkdtemp, mkdir, readFile, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const DAEMON_HOST = '127.0.0.1'
const DAEMON_PORT = 17373
const WEB_HOST = '127.0.0.1'
const WEB_PORT = 14173
const here = dirname(fileURLToPath(import.meta.url))
const webRoot = resolve(here, '..')
const repositoryRoot = resolve(webRoot, '..')
const runtimeRoot = await mkdtemp(join(tmpdir(), 'drove-e2e-'))
const dataDir = join(runtimeRoot, 'data')
const configPath = join(runtimeRoot, 'config.json')
const daemonBinary = join(runtimeRoot, 'droved')

let daemon
let vite
let shuttingDown = false

async function main() {
  await mkdir(dataDir, { recursive: true, mode: 0o700 })
  await writeFile(
    configPath,
    JSON.stringify(
      {
        data_dir: dataDir,
        api_bind: `${DAEMON_HOST}:${DAEMON_PORT}`,
        event_buffer: 1024,
        console_origins: [
          `http://${WEB_HOST}:${WEB_PORT}`,
          `http://localhost:${WEB_PORT}`,
        ],
        agents: {
          claude: { signal_injection: 'off' },
          codex: { signal_injection: 'off' },
        },
        storage: { output_retention_days: 30 },
        session: {
          auto_resume_on_start: false,
          termination_grace_seconds: 0,
        },
        notify: {
          on: ['blocked'],
          debounce_seconds: 30,
          quiet_when_active: true,
          web_push: {
            enabled: true,
            vapid_subject: 'mailto:e2e@example.com',
          },
          ntfy: {
            enabled: false,
            base_url: '',
            topic: '',
            token_file: '',
          },
        },
      },
      null,
      2,
    ),
    { mode: 0o600 },
  )

  await run('go', ['build', '-o', daemonBinary, './cmd/droved'], repositoryRoot)

  daemon = spawn(daemonBinary, ['--config', configPath], {
    cwd: repositoryRoot,
    env: { ...process.env, DROVE_DATA_DIR: dataDir },
    stdio: ['ignore', 'pipe', 'pipe'],
  })
  daemon.stdout.pipe(process.stdout)
  daemon.stderr.pipe(process.stderr)
  daemon.once('exit', (code, signal) => {
    if (shuttingDown) return
    process.stderr.write(
      `E2E daemon exited before shutdown: code=${String(code)} signal=${String(signal)}\n`,
    )
    void shutdown(1)
  })

  await waitForDaemon()

  process.env.DROVE_CONFIG = configPath
  process.env.DROVE_DAEMON = `http://${DAEMON_HOST}:${DAEMON_PORT}`
  process.env.DROVE_DATA_DIR = dataDir
  const { createServer } = await import('vite')
  vite = await createServer({
    root: webRoot,
    configFile: join(webRoot, 'vite.config.ts'),
    server: {
      host: WEB_HOST,
      port: WEB_PORT,
      strictPort: true,
    },
  })
  await vite.listen()
  process.stdout.write(`Drove E2E server ready at http://${WEB_HOST}:${WEB_PORT}\n`)
}

async function run(command, args, cwd) {
  const child = spawn(command, args, { cwd, stdio: 'inherit' })
  const [code, signal] = await once(child, 'exit')
  if (code !== 0) {
    throw new Error(
      `${command} failed: code=${String(code)} signal=${String(signal)}`,
    )
  }
}

async function waitForDaemon() {
  const deadline = Date.now() + 20_000
  let lastError
  while (Date.now() < deadline) {
    try {
      const token = (await readFile(join(dataDir, 'control.token'), 'utf8')).trim()
      const response = await fetch(
        `http://${DAEMON_HOST}:${DAEMON_PORT}/api/v1/agents`,
        { headers: { Authorization: `Bearer ${token}` } },
      )
      if (response.ok) return
      lastError = new Error(`daemon readiness returned ${response.status}`)
    } catch (error) {
      lastError = error
    }
    await delay(100)
  }
  throw new Error(`daemon did not become ready: ${String(lastError)}`)
}

function delay(milliseconds) {
  return new Promise((resolveDelay) => {
    setTimeout(resolveDelay, milliseconds)
  })
}

async function shutdown(code) {
  if (shuttingDown) return
  shuttingDown = true
  try {
    await vite?.close()
    if (daemon !== undefined && daemon.exitCode === null) {
      daemon.kill('SIGTERM')
      await Promise.race([once(daemon, 'exit'), delay(5_000)])
      if (daemon.exitCode === null) daemon.kill('SIGKILL')
    }
  } finally {
    await rm(runtimeRoot, { recursive: true, force: true })
    process.exit(code)
  }
}

process.once('SIGINT', () => {
  void shutdown(0)
})
process.once('SIGTERM', () => {
  void shutdown(0)
})

try {
  await main()
  await new Promise(() => {})
} catch (error) {
  process.stderr.write(`${error instanceof Error ? error.stack : String(error)}\n`)
  await shutdown(1)
}
