import { baseURL, expect, sameOrigin, signIn, signInPage, test } from './fixtures'

// The README's screenshots: `./dev screenshots` runs this against a fresh e2e stack and writes docs/screenshots/.
// Skipped in the normal run.

test.skip(!process.env.SCREENSHOTS, 'set SCREENSHOTS=1 (./dev screenshots)')
test.use({ viewport: { width: 1440, height: 900 } })

const out = (name: string) => `/src/docs/screenshots/${name}.png`

test('screenshots for the README', async ({ page, playwright }) => {
  test.setTimeout(300_000)
  const admin = await playwright.request.newContext({ baseURL })
  const csrf = await signIn(admin)
  const headers = { ...sameOrigin, 'X-CSRF-Token': csrf }
  const made = await admin.post('/api/v1/streams', {
    headers,
    data: { name: 'test', title: 'Studio camera', public: true },
  })
  expect(made.status(), await made.text()).toBe(201)
  const { id } = (await made.json()) as { id: number }
  for (const [name, title] of [
    ['live/stage', 'Main stage'],
    ['live/guest', 'Guest interview'],
  ]) {
    await admin.post('/api/v1/streams', { headers, data: { name, title } })
  }
  // A minute and a half of live data, so the charts show something.
  await new Promise((r) => setTimeout(r, 90_000))

  await signInPage(page, '/')
  await expect(page.getByTestId('online-path-test')).toBeVisible({ timeout: 60_000 })
  await expect(page.getByTestId('stat-traffic')).not.toContainText('–')
  await page.screenshot({ path: out('dashboard') })

  // A 2×2 multi-view of the test pattern.
  await page.goto('/watch')
  await page.getByLabel('Grid').selectOption('2')
  for (let i = 1; i <= 4; i++) {
    await page.getByLabel(`Stream for tile ${String(i)}`).selectOption('test')
  }
  await expect(page.getByTestId('player-mode')).toHaveCount(4)
  await page.waitForFunction(() =>
    [...document.querySelectorAll('video')].every((v) => v.currentTime > 1),
  )
  await page.screenshot({ path: out('watch') })

  // A stream's page, from the top (its encoder settings and health).
  await page.goto(`/streams/${String(id)}`)
  await expect(page.getByRole('heading', { name: 'Studio camera' })).toBeVisible()
  await page.screenshot({ path: out('stream') })

  await page.goto('/config/quick')
  await expect(page.getByRole('heading', { level: 1 })).toBeVisible()
  await page.screenshot({ path: out('configuration') })

  await page.goto('/logs')
  await expect(page.getByTestId('log-line').first()).toBeVisible()
  await page.screenshot({ path: out('logs') })
})
