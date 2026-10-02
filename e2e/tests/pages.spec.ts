import AxeBuilder from '@axe-core/playwright'
import type { Page } from '@playwright/test'

import { expect, signInPage, test } from './fixtures'

// Every page renders (the fixture fails on CSP violations and page errors) and has no serious or critical
// accessibility problems by axe's rules, in the light and the dark theme.

async function axe(page: Page, what: string) {
  // Let transitions (a sheet sliding in or out) finish first: half-faded text fails the contrast rule. Endless
  // animations (the live dot) do not count.
  await page.waitForFunction(() =>
    document.getAnimations().every((a) => a.effect?.getTiming().iterations === Infinity),
  )
  const { violations } = await new AxeBuilder({ page }).analyze()
  const serious = violations
    .filter((v) => v.impact === 'serious' || v.impact === 'critical')
    .map((v) => `${v.id}: ${v.help} (${v.nodes.map((n) => n.target.join(' ')).join(', ')})`)
  expect(serious, what).toEqual([])
}

// Independent tests: they run side by side.
test.describe.configure({ mode: 'parallel' })

test('the sign-in page', async ({ page }) => {
  await page.goto('/login')
  await expect(page.getByRole('heading', { name: 'Sign in to MediaMTX UI' })).toBeVisible()
  await axe(page, 'sign-in')
})

async function inTheme(page: Page, theme: 'Light' | 'Dark', path: string) {
  await signInPage(page, path)
  await page.getByRole('button', { name: 'Theme' }).click()
  await page.getByRole('menuitemradio', { name: theme }).click()
  await page.keyboard.press('Escape')
  expect(await page.evaluate(() => document.documentElement.classList.contains('dark'))).toBe(
    theme === 'Dark',
  )
}

const mainNav = (page: Page, name: string) =>
  page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name }).click()

// Each theme's tour in three parts, so they run side by side.
for (const theme of ['Light', 'Dark'] as const) {
  test(`the live pages, ${theme.toLowerCase()} theme`, async ({ page }) => {
    test.setTimeout(90_000)
    await inTheme(page, theme, '/')
    await expect(page.getByTestId('online-path-test')).toBeVisible({ timeout: 60_000 })
    await expect(page.getByTestId('stat-traffic')).toBeVisible({ timeout: 15_000 })
    await axe(page, 'dashboard')

    await mainNav(page, 'Paths')
    await expect(page.getByTestId('path-row-test')).toBeVisible()
    await axe(page, 'paths')

    await page.getByTestId('path-row-test').getByRole('link', { name: 'test' }).click()
    await expect(page.getByRole('heading', { name: 'test' })).toBeVisible()
    await expect(page.getByText('H264')).toBeVisible()
    await axe(page, 'path')

    await page.getByRole('link', { name: 'RTSP session' }).click()
    await expect(page).toHaveURL(/\/connections\/rtsp\?kind=rtspSessions&id=/)
    await expect(page.getByTestId('connection-detail')).toContainText('devpub')
    await axe(page, 'connection detail')
    await page.keyboard.press('Escape')
    await expect(page.getByTestId('connection-detail')).toHaveCount(0) // gone, not still fading out
    await expect(page.getByRole('table', { name: 'RTSP sessions' })).toBeVisible()
    await axe(page, 'connections')

    // SRT stays off in the e2e stack (streams.spec.ts switches RTMP on while this runs).
    await page.getByRole('link', { name: 'SRT off' }).click()
    await expect(page.getByTestId('off-srtConns')).toBeVisible()
  })

  test(`the admin pages, ${theme.toLowerCase()} theme`, async ({ page }) => {
    await inTheme(page, theme, '/credentials')
    await expect(page.getByRole('table', { name: 'Credentials' })).toBeVisible()
    await axe(page, 'credentials')

    await mainNav(page, 'People')
    await expect(page.getByRole('table', { name: 'People' })).toBeVisible()
    await axe(page, 'people')

    await mainNav(page, 'Streams')
    await expect(page.getByRole('heading', { name: 'Streams' })).toBeVisible()
    await axe(page, 'streams')

    await mainNav(page, 'Exposure')
    await expect(page.getByRole('list', { name: 'Stream ports' })).toBeVisible()
    // "Open…" when closed, "Change…" when open (the viewers rule opens RTSP while the test stream is live).
    await page
      .getByTestId('exposure-rtsp')
      .getByRole('button', { name: /^(Open|Change)…$/ })
      .click()
    await axe(page, 'exposure')

    await page.goto('/no/such/page')
    await expect(page.getByRole('heading', { name: 'Page not found' })).toBeVisible()
    await axe(page, 'not found')
  })

  test(`the configuration pages, ${theme.toLowerCase()} theme`, async ({ page }) => {
    await inTheme(page, theme, '/config')
    const configNav = page.getByRole('navigation', { name: 'Configuration' })
    for (const tab of [
      'Quick setup',
      'Paths',
      'Global settings',
      'Path defaults',
      'YAML',
      'History',
    ]) {
      await configNav.getByRole('link', { name: tab }).click()
      await expect(configNav.getByRole('link', { name: tab })).toHaveAttribute(
        'aria-current',
        'page',
      )
      await expect(page.locator('[data-slot=skeleton]')).toHaveCount(0)
      await axe(page, `config: ${tab}`)
    }
  })
}

test('the layout works on a phone', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await signInPage(page)
  await expect(page.getByRole('navigation', { name: 'Main' })).toBeHidden()
  await page.getByRole('button', { name: 'Open navigation' }).click()
  await page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Paths' }).click()
  await expect(page.getByRole('heading', { name: 'Paths' })).toBeVisible()
  await expect(page.getByRole('navigation', { name: 'Main' })).toBeHidden()
  const overflow = await page.evaluate(
    () => document.documentElement.scrollWidth - window.innerWidth,
  )
  expect(overflow, 'horizontal scrolling').toBeLessThanOrEqual(0)
  await axe(page, 'paths on a phone')
})
