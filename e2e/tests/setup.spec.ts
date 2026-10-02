import { adminPassword, expect, sameOrigin, setupToken, test } from './fixtures'

// The first-run wizard, on a stack that has never been set up. The tests run in order.
test.describe.configure({ mode: 'serial' })

test('a fresh install sends every page to the setup wizard', async ({ page }) => {
  expect(setupToken, 'SETUP_TOKEN (./dev e2e reads it from the sidecar log)').toMatch(
    /^[A-Z2-7]{4}(-[A-Z2-7]{4}){7}$/,
  )
  for (const path of ['/', '/login']) {
    await page.goto(path)
    await expect(page).toHaveURL(/\/setup$/)
  }
  await expect(page.getByRole('heading', { name: 'Set up MediaMTX UI' })).toBeVisible()
})

test('setup refuses a missing or wrong token', async ({ page, request }) => {
  const body = {
    username: 'admin',
    password: adminPassword,
    ingest: { rtsp: true, rtmp: false, srt: false },
  }
  for (const token of ['', 'AAAA-BBBB-CCCC-DDDD-EEEE-FFFF-GGGG-HHHH']) {
    const res = await request.post('/api/v1/setup', {
      data: { ...body, token },
      headers: sameOrigin,
    })
    expect(res.status()).toBe(401)
  }

  await page.goto('/setup')
  await page.getByLabel('Setup token').fill('WRONG-TOKEN')
  await page.getByLabel('Password', { exact: true }).fill(adminPassword)
  await page.getByLabel('Password again').fill(adminPassword)
  await page.getByRole('button', { name: /finish setup/ }).click()
  await expect(page.getByRole('alert')).toContainText('The setup token is wrong')
})

test('setup with the token creates the admin, switches RTSP on and signs in', async ({ page }) => {
  await page.goto('/setup')
  await page.getByLabel('Setup token').fill(setupToken.toLowerCase().replaceAll('-', ' ')) // forgiving input
  await page.getByLabel('Password', { exact: true }).fill(adminPassword)
  await page.getByLabel('Password again').fill(adminPassword)
  await page.getByRole('checkbox', { name: 'RTSP' }).click()
  await page.getByRole('button', { name: /finish setup/ }).click()
  await expect(page.getByTestId('signed-in-as')).toHaveText('Signed in as admin (admin)')
  await expect(page).toHaveURL(/\/$/)
})

test('the token is single-use and the wizard is gone afterwards', async ({ page, request }) => {
  const res = await request.post('/api/v1/setup', {
    data: {
      token: setupToken,
      username: 'second',
      password: adminPassword,
      ingest: { rtsp: false, rtmp: false, srt: false },
    },
    headers: sameOrigin,
  })
  expect(res.status()).toBe(404)
  expect(await (await request.get('/api/v1/setup')).json()).toEqual({
    required: false,
    passkeys: true, // the e2e stack offers passkeys on plain HTTP (MTXUI_PASSKEYS=on)
  })
  await page.goto('/setup')
  await expect(page).toHaveURL(/\/login$/)
})
