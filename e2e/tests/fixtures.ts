import { createHmac } from 'node:crypto'
import { appendFileSync, existsSync, readFileSync, rmSync, writeFileSync } from 'node:fs'

import { test as base, expect, type APIRequestContext, type Page } from '@playwright/test'

export { expect }

export const baseURL = process.env.BASE_URL ?? 'http://ui.mtxe2e.test:9080'
export const mediamtxAPI = process.env.MTX_API_URL ?? 'http://mediamtx:9997'
export const adminPassword = process.env.ADMIN_PASSWORD ?? ''
export const setupToken = process.env.SETUP_TOKEN ?? ''

const controlDir = process.env.E2E_CONTROL_DIR ?? ''

/** What ./dev does on request (e2e_control in ./dev). */
export type ControlAction =
  | 'publisher-stop'
  | 'publisher-start'
  | 'mediamtx-restart'
  | 'recordings-fill'
  | 'age-sessions'
  | 'recordings-unfill'
  | 'config-break'
  | 'stats'
  | 'rtmp-publish'
  | 'rtmp-publish-bframes'
  | 'rtmp-try'
  | 'rtmp-try-audio'

/** Asks ./dev to act on the stack; resolves to the time (epoch ms) at which the action had completed. */
export async function control(action: ControlAction): Promise<number> {
  expect(controlDir, 'E2E_CONTROL_DIR').not.toBe('')
  const done = `${controlDir}/${action}.done`
  rmSync(done, { force: true })
  writeFileSync(`${controlDir}/${action}.req`, '')
  await expect.poll(() => existsSync(done), { timeout: 60_000, intervals: [50] }).toBe(true)
  return Number(readFileSync(done, 'utf8'))
}

/**
 * Has ./dev publish a test pattern over RTMP to url (for a minute, or until MediaMTX closes the connection); with
 * bframes, H.264 with B-frames, as OBS's x264 sends by default.
 */
export async function rtmpPublish(url: string, bframes = false) {
  writeFileSync(`${controlDir}/rtmp-publish.url`, url, { mode: 0o600 })
  await control(bframes ? 'rtmp-publish-bframes' : 'rtmp-publish')
}

/**
 * Has ./dev publish to url for up to 5 s in the foreground, video only (with audio: AAC stereo too, as OBS sends);
 * resolves to ffmpeg's exit code (non-zero: refused).
 */
export async function rtmpTry(url: string, audio = false): Promise<number> {
  writeFileSync(`${controlDir}/rtmp-publish.url`, url, { mode: 0o600 })
  await control(audio ? 'rtmp-try-audio' : 'rtmp-try')
  return Number(readFileSync(`${controlDir}/rtmp-try.exit`, 'utf8'))
}

/** Memory use of the stack's containers in MiB, as `docker stats` reports it, through ./dev. */
export async function containerMemory(): Promise<{ sidecar: number; mediamtx: number }> {
  await control('stats')
  const out = { sidecar: NaN, mediamtx: NaN }
  for (const line of readFileSync(`${controlDir}/stats.json`, 'utf8').split('\n')) {
    if (!line.trim()) continue
    const s = JSON.parse(line) as { Name: string; MemUsage: string }
    const m = /^([\d.]+)\s*([KMG]i?B)/.exec(s.MemUsage)
    if (!m) continue
    const unit =
      { KiB: 1 / 1024, KB: 1 / 1024, MiB: 1, MB: 1, GiB: 1024, GB: 1024 }[m[2] ?? ''] ?? NaN
    const mib = Number(m[1]) * unit
    if (s.Name.endsWith('-sidecar-1')) out.sidecar = mib
    if (s.Name.endsWith('-mediamtx-1')) out.mediamtx = mib
  }
  return out
}

/**
 * Notes a secret a test created (a credential's secret or token), for ./dev e2e's check afterwards that none appears in
 * the stack's logs: the logs stand in for the reverse proxy's access log, which records full URLs.
 */
export function recordSecret(secret: string) {
  const dir = process.env.E2E_CONTROL_DIR ?? ''
  if (dir && secret) appendFileSync(`${dir}/secrets`, `${secret}\n`, { mode: 0o600 })
}

// The sidecar accepts state-changing requests only from its own origin; browsers send it, API clients must.
export const sameOrigin = { Origin: baseURL }

// Every test's page fails on a Content Security Policy violation or an uncaught error, across navigations.
export const test = base.extend({
  page: async ({ page }, use) => {
    const problems: string[] = []
    page.on('console', (m) => {
      if (
        m.type() === 'error' &&
        /Content Security Policy|Refused to (load|apply|execute|connect)/.test(m.text())
      ) {
        problems.push(m.text())
      }
    })
    page.on('pageerror', (err) => problems.push(`uncaught: ${err.message}`))
    await use(page)
    expect(problems, 'CSP violations or page errors').toEqual([])
  },
})

/** Signs the API request context in as the admin and returns the session's CSRF token. */
export async function signIn(request: APIRequestContext): Promise<string> {
  const res = await request.post('/api/v1/auth/login', {
    data: { username: 'admin', password: adminPassword },
    headers: sameOrigin,
  })
  expect(res.status(), await res.text()).toBe(200)
  return ((await res.json()) as { csrfToken: string }).csrfToken
}

/** Signs the page in as the admin through the sign-in form and waits for the dashboard. */
export async function signInPage(page: Page, path = '/') {
  await page.goto(path)
  await page.getByLabel('Username').fill('admin')
  await page.getByLabel('Password').fill(adminPassword)
  await page.getByRole('button', { name: 'Sign in' }).click()
  await expect(page.getByTestId('signed-in-as')).toBeAttached()
}

/** Makes a user's sessions look like they proved who they are long ago, so admin-level changes ask again. */
export async function ageSessions(username: string) {
  writeFileSync(`${controlDir}/age-sessions.user`, username, { mode: 0o600 })
  await control('age-sessions')
}

/** The TOTP code (RFC 6238: HMAC-SHA1, 6 digits, 30 s) for a base32 secret, `offset` steps from now. */
export function totpCode(secret: string, offset = 0): string {
  const alphabet = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567'
  let bits = ''
  for (const ch of secret.replace(/[\s=]/g, '').toUpperCase()) {
    bits += alphabet.indexOf(ch).toString(2).padStart(5, '0')
  }
  const key = Buffer.from((bits.match(/.{8}/g) ?? []).map((b) => parseInt(b, 2)))
  const msg = Buffer.alloc(8)
  msg.writeBigUInt64BE(BigInt(Math.floor(Date.now() / 1000 / 30) + offset))
  const mac = createHmac('sha1', key).update(msg).digest()
  const o = (mac[mac.length - 1] ?? 0) & 0x0f
  return String((mac.readUInt32BE(o) & 0x7fffffff) % 1_000_000).padStart(6, '0')
}

/**
 * Makes a person through the API as the admin (invite, then join with the code) and returns their password; their
 * own request context is signed in. Specs that change how someone signs in use their own person, never the admin.
 */
export async function newPerson(
  request: APIRequestContext,
  adminCSRF: string,
  username: string,
  role: 'admin' | 'operator' | 'viewer' | 'streamer',
  join: APIRequestContext,
): Promise<{ password: string; csrf: string }> {
  const inv = await request.post('/api/v1/users', {
    data: { username, role },
    headers: { ...sameOrigin, 'X-CSRF-Token': adminCSRF },
  })
  expect(inv.status(), await inv.text()).toBe(201)
  const { joinCode } = (await inv.json()) as { joinCode: string }
  recordSecret(joinCode)
  const password = `${username} long password 42`
  // Joining is rate-limited per address like setup, and every spec comes from the same one: wait out a refusal.
  const tryJoin = () =>
    join.post('/api/v1/join', { data: { code: joinCode, password }, headers: sameOrigin })
  let res = await tryJoin()
  for (let i = 0; i < 10 && res.status() === 429; i++) {
    await new Promise((r) => setTimeout(r, Number(res.headers()['retry-after'] ?? '1') * 1000))
    res = await tryJoin()
  }
  expect(res.status(), await res.text()).toBe(201)
  return { password, csrf: ((await res.json()) as { csrfToken: string }).csrfToken }
}

/** Signs the page in as someone through the sign-in form. */
export async function signInAs(page: Page, username: string, password: string, path = '/') {
  await page.goto(path)
  await page.getByLabel('Username').fill(username)
  await page.getByLabel('Password').fill(password)
  await page.getByRole('button', { name: 'Sign in', exact: true }).click()
}

/** Signs the page out through the header. */
export async function signOut(page: Page) {
  await page.getByRole('banner').getByRole('button', { name: 'Sign out', exact: true }).click()
  await expect(page.getByRole('heading', { name: 'Sign in to MediaMTX UI' })).toBeVisible()
}
