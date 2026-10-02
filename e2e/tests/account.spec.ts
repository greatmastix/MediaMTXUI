import {
  ageSessions,
  baseURL,
  expect,
  newPerson,
  recordSecret,
  signIn,
  signInAs,
  signOut,
  test,
  totpCode,
} from './fixtures'

// Your account: an authenticator app asked after the password (a recovery code instead, once), and
// step-up: an admin-level change an hour after the session last proved who it is asks "Confirm it's you" first.

test('an authenticator app, then its code at sign-in; a recovery code once', async ({
  page,
  playwright,
}) => {
  const admin = await playwright.request.newContext({ baseURL })
  const me = await newPerson(
    admin,
    await signIn(admin),
    'e2e-totp',
    'viewer',
    await playwright.request.newContext({ baseURL }),
  )

  await signInAs(page, 'e2e-totp', me.password, '/account')
  const app = page.getByRole('region', { name: 'Authenticator app' })
  await app.getByRole('button', { name: 'Set up an authenticator app' }).click()
  await expect(app.getByRole('img', { name: 'QR code for your authenticator app' })).toBeVisible()
  const secret = (await app.getByTestId('totp-secret').innerText()).replace(/\s/g, '')
  recordSecret(secret)
  await app.getByLabel('The code it shows').fill(totpCode(secret))
  await app.getByRole('button', { name: 'Switch on' }).click()
  const codes = (await app.getByTestId('recovery-codes').locator('li').allInnerTexts()).map((c) =>
    c.trim(),
  )
  expect(codes).toHaveLength(10)
  codes.forEach(recordSecret)
  await app.getByRole('button', { name: 'I have saved them' }).click()
  await expect(app.getByTestId('totp-state')).toContainText('10 recovery codes left')

  // Signed out: the password, then the next code (the current one went to switching it on).
  await signOut(page)
  await signInAs(page, 'e2e-totp', me.password)
  await page.getByLabel('Code from your authenticator app').fill(totpCode(secret, 1))
  await page.getByRole('button', { name: 'Sign in', exact: true }).click()
  await expect(page.getByTestId('signed-in-as')).toContainText('e2e-totp')

  // A recovery code signs in once.
  await signOut(page)
  await signInAs(page, 'e2e-totp', me.password)
  await page.getByLabel('Code from your authenticator app').fill(codes[0] ?? '')
  await page.getByRole('button', { name: 'Sign in', exact: true }).click()
  await expect(page.getByTestId('signed-in-as')).toContainText('e2e-totp')
  await signOut(page)
  await signInAs(page, 'e2e-totp', me.password)
  await page.getByLabel('Code from your authenticator app').fill(codes[0] ?? '')
  await page.getByRole('button', { name: 'Sign in', exact: true }).click()
  await expect(page.getByRole('alert')).toBeVisible()
  await expect(page.getByTestId('signed-in-as')).toHaveCount(0)
})

test('an admin-level change asks to confirm it is you; opted out, it does not', async ({
  page,
  playwright,
}) => {
  const admin = await playwright.request.newContext({ baseURL })
  const me = await newPerson(
    admin,
    await signIn(admin),
    'e2e-stepup',
    'admin',
    await playwright.request.newContext({ baseURL }),
  )
  await signInAs(page, 'e2e-stepup', me.password, '/people')
  await expect(page.getByRole('heading', { name: 'People' })).toBeVisible()
  await ageSessions('e2e-stepup')

  const invite = async (name: string) => {
    await page.getByRole('button', { name: 'Invite' }).click()
    const form = page.getByRole('form', { name: 'Invite' })
    await form.getByLabel('Username').fill(name)
    await form.getByRole('button', { name: 'Invite' }).click()
  }
  await invite('e2e-stepup-guest1')
  const dialog = page.getByRole('dialog', { name: "Confirm it's you" })
  await expect(dialog).toBeVisible()
  await dialog.getByLabel('Password').fill('not it')
  await dialog.getByRole('button', { name: 'Confirm' }).click()
  await expect(dialog.getByRole('alert')).toBeVisible()
  await dialog.getByLabel('Password').fill(me.password)
  await dialog.getByRole('button', { name: 'Confirm' }).click()
  await expect(dialog).toBeHidden()
  await expect(page.getByTestId('join-code')).toBeVisible() // the invite went through after confirming
  recordSecret((await page.getByTestId('join-code').innerText()).trim())
  await page.getByRole('button', { name: 'Done' }).click()

  // Opting out (fresh: no question), then an old session changes without asking.
  await page.goto('/account')
  // (the box shows the server's answer, so click it and wait for that rather than uncheck())
  await page.getByRole('checkbox', { name: /Ask me before admin-level changes/ }).click()
  await expect(
    page.getByRole('checkbox', { name: /Ask me before admin-level changes/ }),
  ).not.toBeChecked()
  await ageSessions('e2e-stepup')
  await page.goto('/people')
  await invite('e2e-stepup-guest2')
  await expect(page.getByTestId('join-code')).toBeVisible()
  await expect(dialog).toBeHidden()
  recordSecret((await page.getByTestId('join-code').innerText()).trim())
})
