import type { Page } from '@playwright/test'

import { baseURL, containerMemory, expect, signIn, signInPage, test } from './fixtures'

// A 9-tile multi-view left running (10 minutes without leaking memory or connections). Opt-in,
// since it takes that long: SOAK_MINUTES=10 ./dev e2e tests/soak.spec.ts. Every half minute it samples the stack's
// memory (docker stats, through ./dev), the browser's JS heap, MediaMTX's WebRTC sessions and whether every tile's
// video still advances; afterwards leaving the page must close every session.

const minutes = Number(process.env.SOAK_MINUTES ?? '0')
const tiles = 9

test.skip(!(minutes > 0), 'set SOAK_MINUTES to run the soak test')

interface Sample {
  t: number
  sidecar: number
  mediamtx: number
  heap: number
  sessions: number
  advancing: number
}

async function times(page: Page): Promise<number[]> {
  return page
    .locator('video')
    .evaluateAll((vs) => vs.map((v) => (v as HTMLVideoElement).currentTime))
}

test('a 9-tile multi-view runs without leaking memory or connections', async ({
  page,
  playwright,
}) => {
  test.setTimeout((minutes * 60 + 240) * 1000)
  const request = await playwright.request.newContext({ baseURL })
  await signIn(request)
  const sessions = async () =>
    (
      (await (await request.get('/api/mtx/v3/webrtc/sessions/list')).json()) as {
        items: { id: string }[]
      }
    ).items.map((x) => x.id)
  const before = new Set(await sessions())
  const ours = async () => (await sessions()).filter((id) => !before.has(id)).length

  await signInPage(page, '/watch')
  await page.getByLabel('Grid').selectOption('3')
  for (let i = 1; i <= tiles; i++) {
    await page.getByLabel(`Stream for tile ${String(i)}`).selectOption('test')
  }
  const modes = page.getByTestId('player-mode')
  await expect(modes).toHaveCount(tiles)
  for (let i = 0; i < tiles; i++) {
    await expect(modes.nth(i)).toHaveAttribute('data-mode', 'webrtc', { timeout: 30_000 })
  }

  const samples: Sample[] = []
  const start = Date.now()
  let last = await times(page)
  while (Date.now() - start < minutes * 60_000) {
    await page.waitForTimeout(30_000)
    const now = await times(page)
    const mem = await containerMemory()
    samples.push({
      t: Math.round((Date.now() - start) / 1000),
      ...mem,
      heap:
        (await page.evaluate(
          () =>
            (performance as unknown as { memory?: { usedJSHeapSize: number } }).memory
              ?.usedJSHeapSize ?? 0,
        )) /
        2 ** 20,
      sessions: await ours(),
      advancing: now.filter((x, i) => x > (last[i] ?? 0) + 10).length,
    })
    last = now
  }
  console.log('  t(s)  sidecar MiB  mediamtx MiB  heap MiB  sessions  playing')
  for (const s of samples) {
    console.log(
      `${String(s.t).padStart(6)}  ${s.sidecar.toFixed(1).padStart(11)}  ${s.mediamtx.toFixed(1).padStart(12)}  ${s.heap.toFixed(1).padStart(8)}  ${String(s.sessions).padStart(8)}  ${String(s.advancing).padStart(7)}`,
    )
  }

  // Connections: exactly one WebRTC session per tile the whole time, and every tile kept playing.
  for (const s of samples) {
    expect(s.sessions, `sessions at ${String(s.t)} s`).toBe(tiles)
    expect(s.advancing, `tiles playing at ${String(s.t)} s`).toBe(tiles)
  }
  // Memory: after the first minute's warm-up, the last third of the run averages within 25 % (and 16 MiB) of the
  // second third. A leak proportional to time grows by the same amount in each third; noise does not.
  const third = Math.floor(samples.length / 3)
  expect(third, 'enough samples for a trend (at least 3 minutes)').toBeGreaterThanOrEqual(2)
  const avg = (xs: Sample[], k: 'sidecar' | 'mediamtx' | 'heap') =>
    xs.reduce((a, s) => a + s[k], 0) / xs.length
  const middle = samples.slice(third, 2 * third)
  const end = samples.slice(2 * third)
  for (const k of ['sidecar', 'mediamtx', 'heap'] as const) {
    const a = avg(middle, k)
    const b = avg(end, k)
    console.log(`${k}: ${a.toFixed(1)} MiB -> ${b.toFixed(1)} MiB`)
    expect(b, `${k} memory grows`).toBeLessThanOrEqual(Math.max(a * 1.25, a + 16))
  }

  await page.goto('/paths')
  await expect.poll(ours, { timeout: 15_000 }).toBe(0)
  await request.dispose()
})
