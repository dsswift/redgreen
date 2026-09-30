import tailwindcss from '@tailwindcss/vite'
import react from '@vitejs/plugin-react'
import { defineConfig } from 'vitest/config'

export default defineConfig({
  root: 'src/web',
  publicDir: 'public',
  plugins: [react(), tailwindcss()],
  build: {
    outDir: '../../dist/web',
    emptyOutDir: true,
  },
  server: {
    port: 5173,
    proxy: {
      '/api': { target: 'http://localhost:8080', changeOrigin: false },
      '/setup': 'http://localhost:8080',
      '/healthz': 'http://localhost:8080',
    },
  },
  test: {
    root: '.',
    include: ['src/**/*.test.ts'],
    env: { LOG_LEVEL: 'error' },
  },
})
