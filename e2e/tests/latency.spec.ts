import { execFile } from 'node:child_process'
import { promisify } from 'node:util'

import { adminPassword, baseURL, test } from './fixtures'

// How late live events reach a browser: reads the event stream for a while and compares each history sample's
// timestamp with its arrival (scripts/sse-latency.mjs, also usable against the real host). It only reads, so it runs
// alongside the other specs.

test('live events arrive promptly (p95 under 2.5 s)', async () => {
  test.setTimeout(60_000)
  const { stdout } = await promisify(execFile)(
    'node',
    ['/src/scripts/sse-latency.mjs', baseURL, '22', '2500'],
    {
      env: {
        ...process.env,
        MTXUI_USER: 'admin',
        MTXUI_PASSWORD: adminPassword,
      },
    },
  )
  console.log(`sse-latency: ${stdout.trim()}`)
})
