import { baseURL, expect, recordSecret, sameOrigin, signIn, signInPage, test } from './fixtures'

// Stream credentials against the real stack: a reader opens an HLS session with one; revoking it closes the session
// within 5 s and refuses the credential from then on.

const hls = 'http://mediamtx:8888/test/index.m3u8'

test('a revoked credential is refused and its open session closed within 5 s', async ({
  playwright,
}) => {
  test.setTimeout(90_000)
  const request = await playwright.request.newContext({ baseURL })
  const csrf = await signIn(request)
  const headers = { ...sameOrigin, 'X-CSRF-Token': csrf }
  let res = await request.post('/api/v1/credentials', {
    headers,
    data: { name: 'e2e-reader', actions: ['read'], paths: ['test'] },
  })
  expect(res.status(), await res.text()).toBe(201)
  const { secret } = (await res.json()) as { secret: string }
  recordSecret(secret)
  const auth = { Authorization: `Basic ${Buffer.from(`e2e-reader:${secret}`).toString('base64')}` }

  // The test stream has to be up for HLS to start a session.
  await expect
    .poll(async () => (await request.get(hls, { headers: auth })).status(), { timeout: 60_000 })
    .toBe(200)
  const sessions = async () => {
    const list = (await (await request.get('/api/mtx/v3/hls/sessions/list')).json()) as {
      items: { id: string; user: string }[]
    }
    return list.items.filter((s) => s.user === 'e2e-reader').length
  }
  await expect.poll(sessions, { timeout: 10_000 }).toBeGreaterThan(0)

  res = await request.post('/api/v1/credentials/e2e-reader/revoke', { headers })
  expect(res.status(), await res.text()).toBe(200)
  expect(((await res.json()) as { kicked: number }).kicked).toBeGreaterThan(0)
  const revokedAt = Date.now()
  await expect.poll(sessions, { timeout: 5_000, intervals: [100] }).toBe(0)
  console.log(`revoked credential: its HLS session closed after ${Date.now() - revokedAt} ms`)
  expect((await request.get(hls, { headers: auth })).status()).toBe(401)
  await request.dispose()
})

test('the credentials page creates a credential and shows the secret once', async ({ page }) => {
  await signInPage(page, '/credentials')
  await page.getByRole('button', { name: 'New credential' }).click()
  await page.getByLabel('Name', { exact: true }).fill('e2e-viewer')
  await page.getByLabel('Paths', { exact: true }).fill('test')
  await page.getByRole('button', { name: 'Create' }).click()
  const created = page.getByTestId('cred-created')
  await expect(created.getByTestId('cred-secret')).toHaveText(/^[A-Za-z0-9._~-]{16,}$/)
  recordSecret(await created.getByTestId('cred-secret').innerText())
  await expect(created).toContainText('rtsp://e2e-viewer:')
  await expect(created).toContainText(`${new URL(page.url()).origin}/whep/test`)
  await expect(created.getByTestId('cred-rtc-note')).toContainText('HTTP Basic auth')
  await created.getByRole('button', { name: 'I have copied it' }).click()
  await expect(page.getByTestId('cred-secret')).toHaveCount(0)
  await expect(page.getByTestId('cred-e2e-viewer')).toContainText('active')
})
