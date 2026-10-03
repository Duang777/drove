import { readFileSync } from 'node:fs'
import type { ClientRequest } from 'node:http'
import { homedir } from 'node:os'
import { join } from 'node:path'
import { defineConfig, type ProxyOptions } from 'vite'
import react from '@vitejs/plugin-react'

// Dev server 将 /api 与 /ws 代理到本地 Drove daemon（默认 127.0.0.1:7373）。
const DAEMON_ORIGIN = process.env.DROVE_DAEMON ?? 'http://127.0.0.1:7373'
const DATA_DIR = process.env.DROVE_DATA_DIR ?? join(homedir(), '.drove')
const TOKEN_PATH = join(DATA_DIR, 'control.token')

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
