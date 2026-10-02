import type { Page } from '@playwright/test'

import { baseURL, expect, signIn as signInAPI, signInPage, test } from './fixtures'

// The live view against the real stack: the test pattern plays over WebRTC through the sidecar's WHEP proxy, and
// falls back to HLS when WebRTC cannot connect.

async function playing(page: Page, index = 0) {
  // The video advances: frames are being decoded, not just a connection made.
  const video = page.locator('video').nth(index)
  const t0 = await video.evaluate((v: HTMLVideoElement) => v.currentTime)
  await expect
    .poll(() => video.evaluate((v: HTMLVideoElement) => v.currentTime), { timeout: 15_000 })
    .toBeGreaterThan(t0 + 1)
}

test('the test pattern plays over WebRTC', async ({ page }) => {
  test.setTimeout(90_000)
  await signInPage(page, '/watch?path=test')
  await expect(page.getByTestId('player-mode')).toHaveAttribute('data-mode', 'webrtc', {
    timeout: 30_000,
  })
  await playing(page)
})

test('a WebRTC failure falls back to HLS within 10 s, and says so', async ({ page }) => {
  test.setTimeout(90_000)
  await signInPage(page, '/watch?path=test&ice=relay')
  const started = Date.now()
  await expect(page.getByTestId('player-mode')).toHaveAttribute('data-mode', 'hls', {
    timeout: 10_000,
  })
  console.log(`fell back to HLS after ${Date.now() - started} ms`)
  await expect(page.getByTestId('player-note')).toContainText('WebRTC did not work')
  await playing(page)
})

test('a multi-view grid plays, its layout survives a reload, and closing it leaves no sessions behind', async ({
  page,
  playwright,
}) => {
  test.setTimeout(120_000)
  // Sessions that already exist (earlier tests' browsers, which MediaMTX drops on its own ICE timeout) do not count.
  const probe = await playwright.request.newContext({ baseURL })
  await signInAPI(probe)
  const before = new Set(
    (
      (await (await probe.get('/api/mtx/v3/webrtc/sessions/list')).json()) as {
        items: { id: string }[]
      }
    ).items.map((x) => x.id),
  )
  await probe.dispose()
  await signInPage(page, '/watch')
  await page.getByLabel('Grid').selectOption('2')
  for (let i = 1; i <= 4; i++) {
    await page.getByLabel(`Stream for tile ${String(i)}`).selectOption('test')
  }
  const modes = page.getByTestId('player-mode')
  await expect(modes).toHaveCount(4)
  for (let i = 0; i < 4; i++) {
    await expect(modes.nth(i)).toHaveAttribute('data-mode', 'webrtc', { timeout: 30_000 })
  }
  await playing(page, 3)

  // The layout on screen comes back after leaving the page and returning (without a reload).
  await page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Paths' }).click()
  await page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Watch' }).click()
  await expect(page.getByLabel('Grid')).toHaveValue('2')
  await expect(page.getByLabel('Stream for tile 4')).toHaveValue('test')
  await expect(modes).toHaveCount(4)

  await page.getByLabel('Name', { exact: true }).fill('e2e wall')
  await page.getByRole('button', { name: 'Save layout' }).click()
  await expect(page.getByTestId('watch-message')).toHaveText('Saved as e2e wall.')
  await page.reload()
  await page.getByLabel('Saved layout').selectOption('e2e wall')
  await expect(page.getByTestId('player-mode')).toHaveCount(4)

  // Leaving the page ends every WebRTC session it opened, through the proxy (no 404s: DELETE before closing).
  const request = await playwright.request.newContext({ baseURL: page.url() })
  const { signIn } = await import('./fixtures')
  await signIn(request)
  const list = async () =>
    (
      (await (await request.get('/api/mtx/v3/webrtc/sessions/list')).json()) as {
        items: { id: string }[]
      }
    ).items.map((x) => x.id)
  await expect
    .poll(async () => (await list()).filter((id) => !before.has(id)).length, { timeout: 30_000 })
    .toBe(4)
  await page.goto('/paths')
  await expect
    .poll(async () => (await list()).filter((id) => !before.has(id)).length, { timeout: 10_000 })
    .toBe(0)
  await request.dispose()
})

// MediaMTX's own ICE timeout (about 30 s) is what ends such a session. Slow, so opt-in.
test(
  'a browser that vanishes mid-stream leaves no session behind for long',
  { tag: '@slow' },
  async ({ browser, playwright }) => {
    test.setTimeout(120_000)
    const context = await browser.newContext()
    const page = await context.newPage()
    await signInPage(page, '/watch?path=test')
    await expect(page.getByTestId('player-mode')).toHaveAttribute('data-mode', 'webrtc', {
      timeout: 30_000,
    })
    const request = await playwright.request.newContext({ baseURL })
    const { signIn } = await import('./fixtures')
    await signIn(request)
    const list = async () =>
      (
        (await (await request.get('/api/mtx/v3/webrtc/sessions/list')).json()) as {
          items: { id: string }[]
        }
      ).items.map((x) => x.id)
    const open = new Set(await list())
    // No cleanup at all: the context goes away as a crashed tab would.
    await context.close()
    const gone = Date.now()
    await expect
      .poll(async () => (await list()).filter((id) => open.has(id)).length, {
        timeout: 90_000,
        intervals: [1000],
      })
      .toBe(0)
    console.log(`orphaned WebRTC sessions closed by MediaMTX after ${Date.now() - gone} ms`)
    await request.dispose()
  },
)
