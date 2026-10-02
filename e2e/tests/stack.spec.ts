import { adminPassword, baseURL, expect, mediamtxAPI, signIn, test } from './fixtures'

test('every response carries the security headers', async ({ request }) => {
  for (const path of ['/', '/login', '/api/v1/health', '/api/v1/session', '/assets/missing.js']) {
    const h = (await request.get(path)).headers()
    expect(h['content-security-policy'], path).toContain("default-src 'self'")
    expect(h['content-security-policy'], path).toContain("frame-ancestors 'none'")
    expect(h['content-security-policy'], path).not.toContain('unsafe-inline')
    expect(h['x-content-type-options'], path).toBe('nosniff')
    expect(h['referrer-policy'], path).toBe('same-origin')
    expect(h['permissions-policy'], path).toContain('publickey-credentials-get=(self)')
    expect(h['x-frame-options'], path).toBe('DENY')
  }
})

test('the pages load without CSP violations', async ({ page }) => {
  // The fixture fails the test on any violation; this visits every page there is.
  await page.goto('/login')
  await expect(page.getByRole('heading', { name: 'Sign in to MediaMTX UI' })).toBeVisible()
  await page.getByLabel('Username').fill('admin')
  await page.getByLabel('Password').fill(adminPassword)
  await page.getByRole('button', { name: 'Sign in' }).click()
  await expect(page.getByTestId('mediamtx-status')).toContainText('Running')
  await page.goto('/no/such/page')
  await expect(page.getByRole('heading', { name: 'Page not found' })).toBeVisible()
})

test('the status reports a protected, matching MediaMTX and no warnings', async ({
  playwright,
}) => {
  const request = await playwright.request.newContext({ baseURL })
  await signIn(request)
  await expect
    .poll(
      async () =>
        (
          (await (await request.get('/api/v1/status')).json()) as {
            apiProtected: boolean | null
          }
        ).apiProtected,
    )
    .toBe(true)
  const status = (await (await request.get('/api/v1/status')).json()) as Record<string, unknown>
  expect(status).toMatchObject({
    reachable: true,
    version: process.env.MEDIAMTX_VERSION,
    warnings: [],
  })
  await request.dispose()
})

test("the publisher's stream reaches MediaMTX, seen through the API proxy", async ({
  playwright,
}) => {
  const request = await playwright.request.newContext({ baseURL })
  await signIn(request)
  // Setup switched RTSP on; the publisher retries every 2 s with its credential.
  await expect
    .poll(
      async () =>
        (
          (await (await request.get('/api/mtx/v3/paths/get/test')).json()) as {
            ready?: boolean
          }
        ).ready,
      {
        timeout: 60_000,
      },
    )
    .toBe(true)
  const path = (await (await request.get('/api/mtx/v3/paths/get/test')).json()) as {
    tracks: string[]
  }
  expect(path.tracks).toEqual(['H264', 'Opus'])
  // Operations the table keeps from browsers are not reachable, whatever the role.
  expect((await request.get('/api/mtx/v3/config/global/get')).status()).toBe(404)
  await request.dispose()
})

test('MediaMTX and the internal endpoint refuse other containers', async ({ request }) => {
  expect((await request.get(`${mediamtxAPI}/v3/paths/list`)).status()).toBe(401)
  const auth = await request.post('http://sidecar:9081/internal/auth', {
    data: { user: 'x', password: 'y', action: 'api' },
  })
  expect(auth.status()).toBe(403)
})
