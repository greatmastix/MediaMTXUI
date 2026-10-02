import { expect, signInPage, test } from './fixtures'

// Exposure control against the real loop: the sidecar writes desired.json, the helper (mtx-portgate with the
// dry-run driver, looping in the e2e stack) applies it and reports back, and the page shows what the helper did.

test.describe.configure({ mode: 'serial' })

test('opening a port for named addresses, closing it, and closing everything', async ({ page }) => {
  test.setTimeout(60_000)
  await signInPage(page, '/exposure')
  const rtsp = page.getByTestId('exposure-rtsp')
  // The test publisher is live, so the automatic viewers rule has RTSP open to anyone; switching the rules off
  // closes what they opened.
  await expect(page.getByTestId('auto-openings')).toContainText('viewers of live streams', {
    timeout: 20_000,
  })
  await expect(rtsp).toContainText('to anyone', { timeout: 10_000 })
  for (const rule of [/Let encoders in/, /Remember encoders/, /Let players and viewers in/]) {
    await page.getByRole('checkbox', { name: rule }).uncheck()
  }
  await expect(page.getByTestId('auto-openings')).toHaveCount(0, { timeout: 10_000 })
  await expect(rtsp).toContainText('Closed', { timeout: 10_000 })
  await expect(rtsp).toContainText('8554/tcp')

  // "My address" is refused here: the browser reaches the sidecar from a private address inside the stack.
  await rtsp.getByRole('button', { name: 'Open…' }).click()
  await rtsp.getByRole('button', { name: 'Open RTSP' }).click()
  await expect(rtsp.getByRole('alert')).toContainText('not a public one')

  await rtsp.getByLabel('These addresses or ranges').check()
  await rtsp.getByLabel('Addresses', { exact: true }).fill('203.0.113.7, 198.51.100.0/24')
  await rtsp.getByRole('button', { name: 'Open RTSP' }).click()
  const opened = Date.now()
  await expect(rtsp).toContainText('Open', { timeout: 10_000 })
  await expect(rtsp).toContainText('198.51.100.0/24, 203.0.113.7/32')
  await expect(rtsp).toContainText(/closes in (59m|1h)/)
  console.log(
    `the helper applied the change and the page showed it after ${Date.now() - opened} ms`,
  )

  // A range wider than the policy allows (/16 here) comes back from the sidecar before anything is written.
  const srt = page.getByTestId('exposure-srt')
  await srt.getByRole('button', { name: 'Open…' }).click()
  await srt.getByLabel('These addresses or ranges').check()
  await srt.getByLabel('Addresses', { exact: true }).fill('10.0.0.0/8')
  await srt.getByRole('button', { name: 'Open SRT' }).click()
  await expect(srt.getByRole('alert')).toContainText('wider than /16')
  await srt.getByLabel('Anyone').check()
  await srt.getByRole('button', { name: 'Open SRT' }).click()
  await expect(srt).toContainText('to anyone', { timeout: 10_000 })

  await rtsp.getByRole('button', { name: 'Close', exact: true }).click()
  await expect(rtsp).toContainText('Closed', { timeout: 10_000 })

  await page.getByRole('button', { name: 'Close all exposure' }).click()
  await page.getByRole('button', { name: 'Close all', exact: true }).click()
  await expect(srt).toContainText('Closed', { timeout: 10_000 })
  await expect(page.getByRole('button', { name: 'Close all exposure' })).toBeDisabled()
})
