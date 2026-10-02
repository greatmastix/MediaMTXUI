import { baseURL, expect, newPerson, signIn, signInAs, signOut, test } from './fixtures'

// Passkeys with Chromium's virtual authenticator. WebAuthn needs a secure context: this spec's
// browser treats the stack's plain-HTTP origin as secure (and the e2e sidecar offers passkeys there). The headless
// shell ignores that switch, so this spec runs the full Chromium.

test.use({
  channel: 'chromium',
  launchOptions: { args: [`--unsafely-treat-insecure-origin-as-secure=${baseURL}`] },
})

test('a passkey signs in without a password; a counter that goes back does not; the password stays', async ({
  page,
  playwright,
}) => {
  const admin = await playwright.request.newContext({ baseURL })
  const me = await newPerson(
    admin,
    await signIn(admin),
    'e2e-passkey',
    'viewer',
    await playwright.request.newContext({ baseURL }),
  )
  const cdp = await page.context().newCDPSession(page)
  await cdp.send('WebAuthn.enable')
  const { authenticatorId } = await cdp.send('WebAuthn.addVirtualAuthenticator', {
    options: {
      protocol: 'ctap2',
      transport: 'internal',
      hasResidentKey: true,
      hasUserVerification: true,
      isUserVerified: true,
    },
  })

  await signInAs(page, 'e2e-passkey', me.password, '/account')
  expect(
    await page.evaluate(() => window.isSecureContext && 'PublicKeyCredential' in window),
    'the browser offers WebAuthn on the stack origin',
  ).toBe(true)
  const keys = page.getByRole('region', { name: 'Passkeys' })
  await keys.getByLabel('Name for the new passkey').fill('e2e key')
  await keys.getByLabel('Your password').fill(me.password) // adding a factor takes the password
  await keys.getByRole('button', { name: 'Add a passkey' }).click()
  await expect(keys.getByRole('list', { name: 'Your passkeys' })).toContainText('e2e key')

  // Signed out: the passkey alone signs in, no username, no password.
  await signOut(page)
  await page.getByRole('button', { name: 'Sign in with a passkey' }).click()
  await expect(page.getByTestId('signed-in-as')).toContainText('e2e-passkey')

  // The same key with its signature counter set back (a copied authenticator): refused.
  const { credentials } = await cdp.send('WebAuthn.getCredentials', { authenticatorId })
  const cred = credentials[0]
  if (!cred) throw new Error('no credential on the virtual authenticator')
  await cdp.send('WebAuthn.removeCredential', { authenticatorId, credentialId: cred.credentialId })
  await cdp.send('WebAuthn.addCredential', {
    authenticatorId,
    credential: { ...cred, signCount: 0 },
  })
  await signOut(page)
  await page.getByRole('button', { name: 'Sign in with a passkey' }).click()
  await expect(page.getByRole('alert')).toBeVisible()
  await expect(page.getByTestId('signed-in-as')).toHaveCount(0)

  // Remove the passkey: the password still signs in.
  await signInAs(page, 'e2e-passkey', me.password, '/account')
  await page
    .getByRole('region', { name: 'Passkeys' })
    .getByRole('button', { name: 'Remove' })
    .click()
  await expect(page.getByRole('region', { name: 'Passkeys' }).getByRole('list')).toHaveCount(0)
  await signOut(page)
  await signInAs(page, 'e2e-passkey', me.password)
  await expect(page.getByTestId('signed-in-as')).toContainText('e2e-passkey')
})
