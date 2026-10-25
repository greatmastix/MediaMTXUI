import { availableParallelism } from 'node:os'

import { defineConfig, devices, type Project } from '@playwright/test'

// Runs inside the e2e compose stack (`./dev e2e`), which publishes nothing: the sidecar and MediaMTX are reached by
// their service names on the stack network. The stack starts unconfigured; the "setup" project runs the first-run
// wizard, and everything else depends on it.
//
// Speed: the specs that leave the stack as they found it run in parallel ("app"). The ones that disturb it for
// everyone (MediaMTX exits or restarts, the publisher stops, WebRTC sessions on "test" are counted) run afterwards, one spec at a time, each project waiting for the one before. Tests tagged @slow (waiting out
// MediaMTX's own timeouts) run only with E2E_SLOW=1 (`./dev e2e --slow`).
//
// `./dev e2e --only watch,auth` (E2E_ONLY=watch,auth) runs just those specs after setup, in the order a full run has
// them. Playwright runs a dependency project in full whatever the file filter, so a disruptive spec named on the
// command line would bring every project before it along: the projects themselves are cut down instead.

const browser = { ...devices['Desktop Chrome'] }

/** Disruptive specs, in the order they run. */
const serial = ['config', 'live', 'watch', 'recordings', 'backups']

const only = process.env.E2E_ONLY ? process.env.E2E_ONLY.split(',') : undefined
/** The parallel specs picked with E2E_ONLY, or undefined for all of them. */
const picked = only?.filter((name) => name !== 'setup' && !serial.includes(name))
const withApp = !picked || picked.length > 0
const chained = serial.filter((name) => !only || only.includes(name))

const app: Project = {
  name: 'app',
  ...(picked && { testMatch: new RegExp(`/(${picked.join('|')})\\.spec\\.ts$`) }),
  testIgnore: [/setup\.spec\.ts/, new RegExp(`/(${serial.join('|')})\\.spec\\.ts$`)],
  dependencies: ['setup'],
  use: browser,
}

const chain: Project[] = chained.map((name, i) => ({
  name,
  testMatch: new RegExp(`/${name}\\.spec\\.ts$`),
  dependencies: [chained[i - 1] ?? (withApp ? 'app' : 'setup')],
  use: browser,
}))

export default defineConfig({
  testDir: './tests',
  forbidOnly: !!process.env.CI,
  retries: 0,
  // Each worker is a Chromium with live video: six on a workstation, one fewer than the CPUs on a small CI runner.
  workers: Math.max(2, Math.min(6, availableParallelism() - 1)),
  grepInvert: process.env.E2E_SLOW ? undefined : /@slow/,
  reporter: [['list'], ['html', { open: 'never' }]],
  use: {
    baseURL: process.env.BASE_URL ?? 'http://ui.mtxe2e.test:9080',
    trace: 'retain-on-failure',
  },
  projects: [
    { name: 'setup', testMatch: /setup\.spec\.ts/, use: browser },
    ...(withApp ? [app] : []),
    ...chain,
  ],
})
