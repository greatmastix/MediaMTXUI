/// <reference types="vitest/config" />
import path from 'node:path'

import tailwindcss from '@tailwindcss/vite'
import react from '@vitejs/plugin-react'
import { defineConfig } from 'vite'

// The dev compose stack points this at the sidecar container; outside it, a sidecar on localhost.
const apiTarget = process.env.VITE_API_TARGET ?? 'http://127.0.0.1:9080'
// The sidecar accepts state-changing requests only from PUBLIC_URL's origin, so the dev proxy presents it.
const publicURL = process.env.VITE_PUBLIC_URL ? new URL(process.env.VITE_PUBLIC_URL) : undefined

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: { '@': path.resolve(import.meta.dirname, 'src') },
  },
  server: {
    proxy: {
      '/api': {
        target: apiTarget,
        configure: (proxy) => {
          if (!publicURL) return
          proxy.on('proxyReq', (req) => {
            req.setHeader('host', publicURL.host)
            if (req.getHeader('origin')) req.setHeader('origin', publicURL.origin)
          })
        },
      },
    },
  },
  build: {
    // Never inline assets as data: URIs, so the CSP can stay at img-src/font-src 'self'.
    assetsInlineLimit: 0,
    // One chunk for now; scripts/bundle-budget.mjs enforces the real limit (gzip size of the first page load).
    chunkSizeWarningLimit: 1024,
  },
  test: {
    environment: 'jsdom',
    // ./dev ci runs everything at once, and the settings forms render hundreds of fields: 5 s is too tight then.
    testTimeout: 15_000,
    setupFiles: ['./src/test/setup.ts'],
    restoreMocks: true,
    unstubGlobals: true,
  },
})
