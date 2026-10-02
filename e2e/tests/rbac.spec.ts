import { baseURL, expect, newPerson, sameOrigin, signIn, signInAs, test } from './fixtures'

// Roles: a viewer and an operator reach no admin route, through the API or the pages, and a viewer
// no operator route.

const adminGets = [
  '/api/v1/users',
  '/api/v1/credentials',
  '/api/v1/exposure',
  '/api/v1/config',
  '/api/v1/audit',
]

test('viewers and operators cannot reach admin routes', async ({ playwright, page }) => {
  const admin = await playwright.request.newContext({ baseURL })
  const csrf = await signIn(admin)
  for (const role of ['viewer', 'operator'] as const) {
    const ctx = await playwright.request.newContext({ baseURL })
    const me = await newPerson(admin, csrf, `e2e-rbac-${role}`, role, ctx)
    const headers = { ...sameOrigin, 'X-CSRF-Token': me.csrf }
    for (const path of adminGets) {
      expect((await ctx.get(path)).status(), `${role} GET ${path}`).toBe(403)
    }
    expect(
      (await ctx.post('/api/v1/recordings/guard/release', { headers })).status(),
      `${role} releases the guard`,
    ).toBe(403)
    // allowed (what MediaMTX answers meanwhile does not matter here: other specs reload it)
    expect((await ctx.get('/api/v1/recordings')).status(), `${role} lists recordings`).not.toBe(403)
    const del = await ctx.post('/api/v1/recordings/delete', {
      headers,
      data: { path: 'test', starts: [] },
    })
    if (role === 'viewer') expect(del.status(), 'a viewer deletes recordings').toBe(403)
    else expect(del.status(), 'an operator deletes recordings').not.toBe(403)
  }

  // The pages: no admin entries in the menu, and an admin page says so instead of showing anything.
  await signInAs(page, 'e2e-rbac-operator', 'e2e-rbac-operator long password 42')
  await expect(page.getByTestId('signed-in-as')).toContainText('e2e-rbac-operator')
  const nav = page.getByRole('navigation', { name: 'Main' })
  await expect(nav.getByRole('link', { name: 'Dashboard' })).toBeVisible()
  for (const name of ['People', 'Credentials', 'Exposure', 'Configuration', 'Audit log']) {
    await expect(nav.getByRole('link', { name })).toHaveCount(0)
  }
  await page.goto('/audit')
  await expect(page.getByText('The audit log is for admins.')).toBeVisible()
  await expect(page.getByTestId('audit-row')).toHaveCount(0)
})
