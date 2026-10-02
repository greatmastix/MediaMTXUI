import { baseURL, expect, newPerson, recordSecret, signIn, signInPage, test } from './fixtures'

// Backups: a passphrase, a backup made by hand, a person added afterwards, and the restore that
// takes the server back (the sidecar restarts, everyone signs in again, the person is gone). Last in the serial
// chain: the restart ends every session.

const passphrase = 'e2e backup passphrase 42'

test('a backup is made, checked and restored', async ({ page, playwright }) => {
  test.setTimeout(120_000)
  recordSecret(passphrase)
  const admin = await playwright.request.newContext({ baseURL })
  const csrf = await signIn(admin)

  await signInPage(page, '/backups')
  await expect(page.getByText('Backups start once a passphrase is set.')).toBeVisible()
  await page.getByRole('textbox', { name: 'Passphrase', exact: true }).fill(passphrase)
  await page.getByLabel('Again').fill(passphrase)
  await page.getByRole('button', { name: 'Set passphrase' }).click()
  await expect(page.getByRole('region', { name: 'Schedule' })).toBeVisible()
  await page.getByRole('button', { name: 'Back up now' }).click()
  const rows = page.getByTestId('backup-row')
  await expect(rows).toHaveCount(1)
  await expect(rows.first()).toContainText('Made by hand')

  // Afterwards: a new person, whom the restore takes away again.
  await newPerson(
    admin,
    csrf,
    'e2e-after-backup',
    'viewer',
    await playwright.request.newContext({ baseURL }),
  )

  // An uploaded copy with the wrong passphrase is refused.
  const list = (await (await admin.get('/api/v1/backups')).json()) as {
    backups: { name: string }[]
  }
  const name = list.backups[0]?.name ?? ''
  const file = await admin.get(`/api/v1/backups/${name}/download`)
  expect(file.status()).toBe(200)
  await page.locator('input[type=file]').setInputFiles({
    name: 'copy.mtxbackup',
    mimeType: 'application/octet-stream',
    buffer: await file.body(),
  })
  const check = page.getByRole('form', { name: 'Check the backup' })
  await check.getByLabel("The backup's passphrase").fill('not the passphrase at all')
  await check.getByRole('button', { name: 'Check it' }).click()
  await expect(check.getByRole('alert')).toContainText('does not open this backup')
  await check.getByRole('button', { name: 'Cancel' }).click()

  // The kept backup: checked, previewed, restored.
  await rows.filter({ hasText: 'Made by hand' }).getByRole('button', { name: 'Restore…' }).click()
  await check.getByLabel("The backup's passphrase").fill(passphrase)
  await check.getByRole('button', { name: 'Check it' }).click()
  const preview = page.getByTestId('restore-preview')
  await expect(preview.getByTestId('preview-users')).toContainText('gone again: e2e-after-backup')
  await preview.getByRole('button', { name: 'Restore', exact: true }).click()
  await expect(page.getByTestId('restarting')).toBeVisible()
  await expect(page).toHaveURL(/\/login(\?|$)/, { timeout: 60_000 })

  // Back: the old session is gone, the person too, and the restore is in the audit log.
  expect((await admin.get('/api/v1/users')).status()).toBe(401)
  const again = await playwright.request.newContext({ baseURL })
  await signIn(again)
  const people = (await (await again.get('/api/v1/users')).json()) as { username: string }[]
  expect(people.map((p) => p.username)).not.toContain('e2e-after-backup')
  const audit = (await (await again.get('/api/v1/audit?action=backup.')).json()) as {
    action: string
  }[]
  expect(audit.map((a) => a.action)).toContain('backup.restored')
})
