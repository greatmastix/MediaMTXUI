import { adminPassword, baseURL, expect, sameOrigin, signIn, test } from './fixtures'

test('sign-in, a wrong password, and sign-out', async ({ page }) => {
  await page.goto('/')
  await expect(page).toHaveURL(/\/login$/)
  await page.getByLabel('Username').fill('admin')
  await page.getByLabel('Password').fill('not the password')
  await page.getByRole('button', { name: 'Sign in' }).click()
  await expect(page.getByRole('alert')).toHaveText('Wrong username or password.')

  await page.getByLabel('Password').fill(adminPassword)
  await page.getByRole('button', { name: 'Sign in' }).click()
  await expect(page.getByTestId('signed-in-as')).toHaveText('Signed in as admin (admin)')
  const cookies = await page.context().cookies()
  const session = cookies.find((c) => c.name === 'mtxui_session')
  expect(session?.httpOnly).toBe(true)
  expect(session?.sameSite).toBe('Lax')

  await page.getByRole('button', { name: 'Sign out' }).click()
  await expect(page).toHaveURL(/\/login$/)
  await page.goto('/')
  await expect(page).toHaveURL(/\/login$/)
})

test('a username locks after five failures, whether or not it exists', async ({ request }) => {
  const attempt = (username: string, password: string) =>
    request.post('/api/v1/auth/login', {
      data: { username, password },
      headers: sameOrigin,
    })
  for (let i = 0; i < 5; i++) {
    expect((await attempt('mallory', 'guess number ' + i)).status()).toBe(401)
  }
  const locked = await attempt('mallory', 'guess number 6')
  expect(locked.status()).toBe(429)
  expect((await locked.json()) as object).toMatchObject({ error: 'locked' })
  expect(locked.headers()['retry-after']).toBeTruthy()
  // The lock is per username: the admin is unaffected.
  expect((await attempt('admin', adminPassword)).status()).toBe(200)
})

test('state-changing requests need the UI origin and the session CSRF token', async ({
  playwright,
}) => {
  const request = await playwright.request.newContext({ baseURL })
  const csrf = await signIn(request)
  const logout = (headers: Record<string, string>) =>
    request.post('/api/v1/auth/logout', { headers })

  expect((await logout(sameOrigin)).status()).toBe(403) // no CSRF token
  expect((await logout({ ...sameOrigin, 'X-CSRF-Token': 'forged' })).status()).toBe(403)
  expect((await logout({ Origin: 'https://evil.example', 'X-CSRF-Token': csrf })).status()).toBe(
    403,
  )
  expect((await logout({ 'X-CSRF-Token': csrf })).status()).toBe(403) // no Origin at all
  expect((await request.get('/api/v1/session')).status()).toBe(200) // none of those did anything

  expect((await logout({ ...sameOrigin, 'X-CSRF-Token': csrf })).status()).toBe(204)
  expect((await request.get('/api/v1/session')).status()).toBe(401)
  await request.dispose()
})

test('anonymous requests to the API are refused', async ({ request }) => {
  for (const path of [
    '/api/v1/session',
    '/api/v1/status',
    '/api/v1/events',
    '/api/v1/metrics/history',
    '/api/mtx/v3/paths/list',
    '/api/mtx/v3/info',
  ]) {
    expect((await request.get(path)).status(), path).toBe(401)
  }
  expect((await request.post('/api/v1/auth/logout', { headers: sameOrigin })).status()).toBe(401)
  expect((await request.get('/api/v1/health')).status()).toBe(200)
})
