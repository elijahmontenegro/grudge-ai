import path from 'path'
import { execSync } from 'child_process'
import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

function gitShortSha(): string {
  try {
    return execSync('git rev-parse --short HEAD', { cwd: __dirname })
      .toString()
      .trim()
  } catch {
    return 'unknown'
  }
}

export default defineConfig({
  plugins: [react(), tailwindcss()],
  css: {
    transformer: 'postcss',
  },
  define: {
    __BUILD_COMMIT__: JSON.stringify(gitShortSha()),
    __BUILD_DATE__: JSON.stringify(new Date().toISOString().slice(0, 10)),
  },
  resolve: {
    alias: {
      '@': path.resolve(__dirname, './src'),
    },
  },
  test: {
    environment: 'jsdom',
    globals: true,
    setupFiles: './src/test/setup.ts',
  },
  server: {
    proxy: {
      // Must be a literal IP — Node's DNS won't resolve "grudge.localhost"
      // even though browsers do (RFC 6761 applies to browser stacks, not
      // libuv). Browsers still hit http://grudge.localhost:5173 fine; only
      // the proxy-to-backend hop needs the raw address.
      '/graphql': {
        target: 'http://127.0.0.1:8420',
        ws: true,
        changeOrigin: true,
      },
    },
  },
})
