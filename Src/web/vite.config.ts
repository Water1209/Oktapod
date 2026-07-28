import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import { writeFileSync } from 'node:fs'
import { resolve } from 'node:path'

export default defineConfig({
  plugins: [
    react(),
    {
      name: 'preserve-go-embed-anchor',
      closeBundle() {
        writeFileSync(resolve(import.meta.dirname, 'dist/.embedkeep'), '')
      },
    },
  ],
  server: {
    port: Number(process.env.OKTAPOD_WEB_PORT || 5173),
    strictPort: true,
    proxy: {
      '/api': {
        target: process.env.OKTAPOD_API_PROXY || 'http://127.0.0.1:8787',
        changeOrigin: false,
      },
    },
  },
  build: {
    sourcemap: true,
  },
})
