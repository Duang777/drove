import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// Dev server 将 /api 与 /ws 代理到本地 Drove daemon（默认 127.0.0.1:7373）。
const DAEMON_ORIGIN = process.env.DROVE_DAEMON ?? 'http://127.0.0.1:7373'

export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: {
      '/api': { target: DAEMON_ORIGIN, changeOrigin: true },
      '/ws': {
        target: DAEMON_ORIGIN,
        ws: true,
        changeOrigin: true,
      },
    },
  },
  build: {
    outDir: 'dist',
    sourcemap: true,
  },
})
