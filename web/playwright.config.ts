import { defineConfig, devices } from '@playwright/test'
export default defineConfig({
  testDir: './tests',
  timeout: 60000,
  expect: { timeout: 10000 },
  workers: 1,
  outputDir: '../test-results/browser',
  reporter: [['list']],
  use: {
    ...devices['Desktop Chrome'],
    ignoreHTTPSErrors: true,
    channel: process.env.PLAYWRIGHT_CHANNEL,
    baseURL: process.env.GOST_WEB_URL ?? 'http://127.0.0.1:5173',
    trace: 'off',
    screenshot: 'only-on-failure',
    actionTimeout: 15000,
    navigationTimeout: 15000,
  },
  projects: [
    { name: 'mock', testMatch: 'console.spec.ts' },
    { name: 'real', testMatch: 'real.spec.ts' },
  ],
  webServer: process.env.GOST_WEB_URL
    ? undefined
    : {
        command: 'npm run dev -- --port 5173 --strictPort',
        url: 'http://127.0.0.1:5173',
        reuseExistingServer: !process.env.CI,
      },
})
