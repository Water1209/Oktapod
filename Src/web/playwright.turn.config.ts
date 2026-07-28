import { defineConfig, devices } from '@playwright/test'

export default defineConfig({
  testDir: '../../Test/e2e',
  testMatch: '**/personal-turn.e2e.ts',
  fullyParallel: false,
  workers: 1,
  retries: 0,
  reporter: [['list']],
  use: {
    baseURL: 'http://127.0.0.1:15174',
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
  },
  projects: [{ name: 'desktop-turn', use: { ...devices['Desktop Chrome'] } }],
})
