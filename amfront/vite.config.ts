/// <reference types="vitest/config" />
import { defineConfig, loadEnv } from 'vite'
import vue from '@vitejs/plugin-vue'

export default defineConfig(({ mode }) => {
  // Extra hostnames the dev server accepts, e.g. behind a reverse proxy.
  // Set in the environment or in .env.local (not committed):
  //   AMFRONT_ALLOWED_HOSTS=amfront.example.com,other.example.com
  const env = loadEnv(mode, '.', 'AMFRONT_')
  const allowedHosts = (env.AMFRONT_ALLOWED_HOSTS ?? '')
    .split(',')
    .map((h) => h.trim())
    .filter((h) => h !== '')

  return {
    plugins: [vue()],
    server: { port: 8300, allowedHosts },
    test: { environment: 'happy-dom' },
  }
})
