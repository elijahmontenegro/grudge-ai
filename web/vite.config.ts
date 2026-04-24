import path from 'path'
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

export default defineConfig({
  plugins: [react(), tailwindcss()],
  css: {
    transformer: 'postcss',
  },
  resolve: {
    alias: {
      '@': path.resolve(__dirname, './src'),
    },
  },
  server: {
    proxy: {
      // Must be a literal IP — Node's DNS won't resolve "spidey.localhost"
      // even though browsers do (RFC 6761 applies to browser stacks, not
      // libuv). Browsers still hit http://spidey.localhost:5173 fine; only
      // the proxy-to-backend hop needs the raw address.
      '/graphql': {
        target: 'http://127.0.0.1:8420',
        ws: true,
        changeOrigin: true,
      },
    },
  },
})
