import { defineConfig, devices, type Project } from '@playwright/test'

// Runs inside the e2e compose stack (`./dev e2e`), which publishes nothing: the sidecar and MediaMTX are reached by
// their service names on the stack network. The stack starts unconfigured; the "setup" project runs the first-run
// wizard, and everything else depends on it.
//
// Speed: the specs that leave the stack as they found it run in parallel ("app"). The ones that disturb it for
// everyone (MediaMTX exits or restarts, the publisher stops, WebRTC sessions on "test" are counted) run afterwards, one spec at a time, each project waiting for the one before. Tests tagged @slow (waiting out
// MediaMTX's own timeouts) run only with E2E_SLOW=1 (`./dev e2e --slow`).

const browser = { ...devices['Desktop Chrome'] }

/** Disruptive specs, in the order they run. */
const serial = ['config', 'live', 'watch', 'recordings', 'backups']

const chain: Project[] = serial.map((name, i) => ({
  name,
  testMatch: new RegExp(`/${name}\\.spec\\.ts$`),
  dependencies: [i === 0 ? 'app' : (serial[i - 1] ?? 'app')],
  use: browser,
}))

export default defineConfig({
  testDir: './tests',
  forbidOnly: !!process.env.CI,
  retries: 0,
  workers: 6,
  grepInvert: process.env.E2E_SLOW ? undefined : /@slow/,
  reporter: [['list'], ['html', { open: 'never' }]],
  use: {
    baseURL: process.env.BASE_URL ?? 'http://ui.mtxe2e.test:9080',
    trace: 'retain-on-failure',
  },
  projects: [
    { name: 'setup', testMatch: /setup\.spec\.ts/, use: browser },
    {
      name: 'app',
      testIgnore: [/setup\.spec\.ts/, new RegExp(`/(${serial.join('|')})\\.spec\\.ts$`)],
      dependencies: ['setup'],
      use: browser,
    },
    ...chain,
  ],
})
