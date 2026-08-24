import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// https://vite.dev/config/
export default defineConfig({
  base: '/dashboard-assets/',
  plugins: [react()],
  server: {
    proxy: {
      '/v1/proxy/dashboard-data': 'http://127.0.0.1:39281',
    },
  },
})
