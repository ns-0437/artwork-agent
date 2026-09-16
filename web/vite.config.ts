import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig(({ command }) => ({
  // GitHub Pages serves this as a project site at /artwork-agent/, but the
  // local dev server (docker-compose) still needs to serve from / .
  base: command === 'build' ? '/artwork-agent/' : '/',
  plugins: [react()],
  server: {
    host: true,
    port: 5173,
  },
}))
