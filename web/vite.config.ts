import { defineConfig } from 'vitest/config'
import vue from '@vitejs/plugin-vue'
export default defineConfig({
  plugins: [vue()],
  build: { outDir: '../internal/webui/assets', emptyOutDir: true, sourcemap: false },
  server: { proxy: { '/api': { target: 'http://127.0.0.1:18080', changeOrigin: false } } },
  test: { environment: 'jsdom', include: ['src/**/*.test.ts'] },
})
