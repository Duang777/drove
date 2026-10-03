import { existsSync, readFileSync } from 'node:fs'
import type { ClientRequest } from 'node:http'
import { homedir } from 'node:os'
import { join } from 'node:path'
import { defineConfig, type ProxyOptions } from 'vite'
import react from '@vitejs/plugin-react'

const DEFAULT_DATA_DIR = join(homedir(), '.drove')
const CONFIG_PATH = process.env.DROVE_CONFIG ?? join(DEFAULT_DATA_DIR, 'config.json')
const config = readDroveConfig(CONFIG_PATH)
const DAEMON_ORIGIN =
  process.env.DROVE_DAEMON ?? `http://${optionalString(config, 'api_bind') ?? '127.0.0.1:7373'}`
const DATA_DIR =
  process.env.DROVE_DATA_DIR ?? optionalString(config, 'data_dir') ?? DEFAULT_DATA_DIR
const TOKEN_PATH = join(DATA_DIR, 'control.token')

function readDroveConfig(path: string): Record<string, unknown> {
  if (!existsSync(path)) return {}
  const parsed: unknown = JSON.parse(readFileSync(path, 'utf8'))
  if (!isRecord(parsed)) {
    throw new Error(`Invalid Drove config object: ${path}`)
  }
  return parsed
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function optionalString(config: Record<string, unknown>, key: string): string | undefined {
  const value = config[key]
  if (value === undefined) return undefined
  if (typeof value !== 'string' || value.length === 0) {
    throw new Error(`Invalid Drove config field ${key}: ${CONFIG_PATH}`)
  }
  return value
}

function controlAuthorization(): string {
  const token = readFileSync(TOKEN_PATH, 'utf8')
  if (!/^[a-f0-9]{64}$/.test(token)) {
    throw new Error(`Invalid Drove control token: ${TOKEN_PATH}`)
  }
  return `Bearer ${token}`
}

function daemonProxy(websocket = false): ProxyOptions {
  return {
    target: DAEMON_ORIGIN,
    changeOrigin: true,
    ws: websocket,
    configure(proxy) {
      const authorize = (request: ClientRequest) => {
        try {
          request.setHeader('Authorization', controlAuthorization())
        } catch (error) {
          request.destroy(error instanceof Error ? error : new Error(String(error)))
        }
      }
      proxy.on('proxyReq', authorize)
      proxy.on('proxyReqWs', authorize)
    },
  }
}

export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    strictPort: true,
    proxy: {
      '/api': daemonProxy(),
      '/ws': daemonProxy(true),
    },
  },
  build: {
    outDir: 'dist',
    sourcemap: true,
  },
})
