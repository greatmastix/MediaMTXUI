import type { Page } from '@playwright/test'

import { adminPassword, control, expect, sameOrigin, signInPage, test } from './fixtures'

// The live UI against the real stack: the test publisher is stopped and started, and MediaMTX restarted, by ./dev
// on request (see e2e_control in ./dev), while the page is watched. Times are measured in the page, not with
// Playwright's polling, which would add its own intervals.

/** Resolves to the time (epoch ms) at which the element with testId is present (or absent) in the page. */
function when(page: Page, testId: string, present: boolean): Promise<number> {
  return page.evaluate(
    ([id, want]) =>
      new Promise<number>((resolve) => {
        const check = () => {
          if (!!document.querySelector(`[data-testid="${id}"]`) === want) {
            observer.disconnect()
            resolve(Date.now())
          }
        }
        const observer = new MutationObserver(check)
        observer.observe(document.body, { childList: true, subtree: true })
        check()
      }),
    [testId, present] as const,
  )
}

test.describe.configure({ mode: 'serial' })

test('the dashboard shows the publisher within 3 s of it starting and drops it within 3 s of it stopping', async ({
  page,
}) => {
  test.setTimeout(120_000)
  await signInPage(page)
  await expect(page.getByTestId('online-path-test')).toBeVisible({
    timeout: 60_000,
  })
  await expect(page.getByTestId('live-indicator')).toHaveAttribute('data-state', 'live')

  // Stopping is measured from the request to ./dev, so the time Docker takes to stop the container counts too: an
  // upper bound. Starting is measured from the moment MediaMTX put the path online (its own onlineTime, on the same
  // host clock): `docker start` alone can take longer than the whole way from MediaMTX to the page.
  const gone = when(page, 'online-path-test', false)
  const stopRequested = Date.now()
  await control('publisher-stop')
  const droppedAfter = (await gone) - stopRequested

  const back = when(page, 'online-path-test', true)
  const startRequested = Date.now()
  await control('publisher-start')
  const shownAt = await back
  const path = (await (await page.request.get('/api/mtx/v3/paths/get/test')).json()) as {
    onlineTime: string
  }
  const shownAfter = shownAt - Date.parse(path.onlineTime)

  console.log(
    `publisher stop requested: gone after ${droppedAfter} ms; path online: shown after ${shownAfter} ms ` +
      `(${shownAt - startRequested} ms after the start was requested)`,
  )
  expect(droppedAfter).toBeLessThan(3000)
  expect(shownAfter).toBeLessThan(3000)
})

test('ending the session elsewhere sends the page to sign-in, and back afterwards', async ({
  page,
}) => {
  test.setTimeout(60_000)
  await signInPage(page, '/paths')
  await expect(page.getByRole('heading', { name: 'Paths' })).toBeVisible()
  // page.request shares the page's cookies: this is the same session, ended from somewhere else.
  const { csrfToken } = (await (await page.request.get('/api/v1/session')).json()) as {
    csrfToken: string
  }
  const res = await page.request.post('/api/v1/auth/logout', {
    headers: { ...sameOrigin, 'X-CSRF-Token': csrfToken },
  })
  expect(res.status()).toBe(204)
  await expect(page).toHaveURL(/\/login\?redirect=%2Fpaths$/, {
    timeout: 20_000,
  })

  await page.getByLabel('Username').fill('admin')
  await page.getByLabel('Password').fill(adminPassword)
  await page.getByRole('button', { name: 'Sign in' }).click()
  await expect(page).toHaveURL(/\/paths$/)
})

test('a MediaMTX restart shows as degraded and recovers without a reload', async ({ page }) => {
  test.setTimeout(120_000)
  await signInPage(page)
  await expect(page.getByTestId('online-path-test')).toBeVisible({
    timeout: 60_000,
  })
  await page.evaluate(() => {
    ;(window as unknown as { notReloaded?: boolean }).notReloaded = true
  })

  const restarted = control('mediamtx-restart')
  await expect(page.getByTestId('live-indicator')).toHaveAttribute('data-state', 'degraded', {
    timeout: 15_000,
  })
  await expect(page.getByTestId('mediamtx-unreachable')).toBeVisible()
  await restarted
  await expect(page.getByTestId('live-indicator')).toHaveAttribute('data-state', 'live', {
    timeout: 30_000,
  })
  await expect(page.getByTestId('mediamtx-unreachable')).toBeHidden()
  // The publisher reconnects by itself; the dashboard picks it up again.
  await expect(page.getByTestId('online-path-test')).toBeVisible({
    timeout: 60_000,
  })
  expect(
    await page.evaluate(() => (window as unknown as { notReloaded?: boolean }).notReloaded),
  ).toBe(true)
})
