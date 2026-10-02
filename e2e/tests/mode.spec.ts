import { expect, signInPage, test } from './fixtures'

// Simple and expert modes: an admin switches to the streaming view (Streams and Watch, starting on
// Streams), the choice survives a reload, and switching back brings the dashboard and the full menu.
test('an admin switches between the streaming and the server view', async ({ page }) => {
  await signInPage(page, '/')
  const nav = page.getByRole('navigation', { name: 'Main' })
  const mode = page.getByRole('group', { name: 'Mode' })
  await expect(nav.getByRole('link', { name: 'Configuration' })).toBeVisible()

  await mode.getByRole('button', { name: 'Streaming' }).click()
  await expect(page).toHaveURL(/\/streams$/)
  await expect(nav.getByRole('link')).toHaveText(['Streams', 'Watch', 'Account'])
  await expect(mode.getByRole('button', { name: 'Streaming' })).toHaveAttribute(
    'aria-pressed',
    'true',
  )

  await page.goto('/')
  await expect(page).toHaveURL(/\/streams$/)

  await mode.getByRole('button', { name: 'Server' }).click()
  await expect(page).toHaveURL(/\/$/)
  await expect(page.getByRole('heading', { name: 'Dashboard' })).toBeVisible()
  await expect(nav.getByRole('link', { name: 'Configuration' })).toBeVisible()
})
