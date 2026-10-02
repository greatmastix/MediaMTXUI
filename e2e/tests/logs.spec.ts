import { baseURL, expect, sameOrigin, signIn, signInPage, test } from './fixtures'

// Logs and history: the log viewer follows MediaMTX's log live, filters it and the sidecar's own
// lines, and searches; the charts' longer ranges read the stored history.

test('the log viewer follows MediaMTX live, filters, and searches', async ({ page, request }) => {
  await signInPage(page, '/logs')
  await expect(page.getByTestId('log-state')).toContainText('Following live')
  const lines = page.getByTestId('log-line')
  await expect(lines.first()).toBeVisible()

  // New lines, written while the page follows: an HLS request without credentials, which MediaMTX logs and refuses.
  const hls = lines.filter({ hasText: /\[HLS\] \[session \w+\] created by/ })
  const before = await hls.count()
  expect((await request.get('http://mediamtx:8888/test/index.m3u8')).status()).toBe(401)
  await expect(async () => {
    expect(await hls.count()).toBeGreaterThan(before)
  }).toPass({ timeout: 5000 })

  // Errors only.
  await page.getByLabel('Level').selectOption('error')
  await expect(page.getByTestId('log-state')).toContainText('Following live')
  for (const level of await lines.evaluateAll((ls) =>
    ls.map((l) => l.getAttribute('data-level')),
  )) {
    expect(level).toBe('error')
  }

  // The sidecar's own lines.
  await page.getByLabel('Level').selectOption('')
  await page.getByRole('combobox', { name: 'Log', exact: true }).selectOption('sidecar')
  await expect(lines.filter({ hasText: 'request' }).first()).toBeVisible()

  // A search, rotated copies included.
  await page.getByRole('combobox', { name: 'Log', exact: true }).selectOption('mediamtx')
  await page.getByRole('button', { name: 'Stop following' }).click()
  await page.getByLabel('Containing').fill('[hls] [session')
  await page.getByRole('button', { name: 'Show' }).click()
  await expect(page.getByTestId('log-state')).toContainText(/^[1-9]\d* lines/)
  for (const text of await lines.allInnerTexts())
    expect(text.toLowerCase()).toContain('[hls] [session')
})

test('the log download and the history ranges', async ({ page, playwright }) => {
  const request = await playwright.request.newContext({ baseURL })
  await signIn(request)
  const log = await request.get('/api/v1/logs/download')
  expect(log.status()).toBe(200)
  expect(await log.text()).toMatch(/^\d{4}\/\d{2}\/\d{2} \d{2}:\d{2}:\d{2} (INF|WAR|ERR|DEB) /m)
  for (const range of ['24h', '7d', '30d']) {
    const res = await request.get(`/api/v1/metrics/history?range=${range}`)
    expect(res.status(), range).toBe(200)
  }
  expect(
    (await request.get('/api/v1/metrics/history?range=1y', { headers: sameOrigin })).status(),
  ).toBe(400)

  await signInPage(page)
  const history = page.getByRole('region', { name: 'History' })
  await history.getByRole('button', { name: '7 d' }).click()
  await expect(history.getByRole('button', { name: '7 d' })).toHaveAttribute('aria-pressed', 'true')
  await expect(history.getByRole('img', { name: /^Bandwidth, last 7 days/ })).toBeVisible()
  await history.getByRole('button', { name: '1 h' }).click()
  await expect(history.getByRole('img', { name: /^Bandwidth, last/ })).not.toHaveAccessibleName(
    /days/,
  )
})
