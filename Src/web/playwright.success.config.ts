import { defineConfig, devices } from '@playwright/test'
const webURL = 'http://127.0.0.1:15174'

export default defineConfig({
  testDir: '../../Test/e2e',
  testMatch: ['**/personal-success.e2e.ts', '**/workbench-routes.e2e.ts'],
  fullyParallel: false,
  retries: process.env.CI ? 1 : 0,
  reporter: [['list'], ['html', { open: 'never', outputFolder: 'playwright-report-success' }]],
  use: {
    baseURL: webURL,
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
  },
  projects: [
    { name: 'chromium-success', use: { ...devices['Desktop Chrome'] } },
  ],
})
