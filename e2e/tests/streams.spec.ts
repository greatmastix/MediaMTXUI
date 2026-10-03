import type { Browser } from '@playwright/test'

import {
  baseURL,
  expect,
  recordSecret,
  rtmpPublish,
  rtmpTry,
  sameOrigin,
  signIn,
  signInPage,
  test,
} from './fixtures'

// Stream pages and streamer accounts against the real stack: an admin invites a streamer and
// gives them a stream; the streamer joins with the code, takes the server and key from the page, publishes over RTMP
// with ffmpeg (from the test publisher's container, through ./dev), sees it live, makes a new key (which kicks the
// encoder and retires the old key), and sees nothing of anyone else's.

test.describe.configure({ mode: 'serial' })

let joinCode = ''
let otherID = 0

test('an admin invites a streamer and gives them a stream', async ({ page, playwright }) => {
  test.setTimeout(60_000)
  const request = await playwright.request.newContext({ baseURL })
  const csrf = await signIn(request)
  const headers = { ...sameOrigin, 'X-CSRF-Token': csrf }
  // RTMP on, so the page offers it (setup switched on RTSP only).
  const res = await request.patch('/api/v1/config/global', {
    headers,
    data: { set: { rtmp: true } },
  })
  expect([200], await res.text()).toContain(res.status())
  const other = await request.post('/api/v1/streams', {
    headers,
    data: { name: 'e2e/other', title: 'Someone else', public: false },
  })
  expect(other.status(), await other.text()).toBe(201)
  otherID = ((await other.json()) as { id: number }).id
  await request.dispose()

  await signInPage(page, '/people')
  await page.getByRole('button', { name: 'Invite' }).click()
  await page.getByLabel('Username').fill('e2e-streamer')
  await page.getByRole('form', { name: 'Invite' }).getByRole('button', { name: 'Invite' }).click()
  joinCode = (await page.getByTestId('join-code').innerText()).trim()
  expect(joinCode).toMatch(/^[A-Z2-7]{4}(-[A-Z2-7]{4}){3}$/)
  recordSecret(joinCode)
  recordSecret(joinCode.replaceAll('-', ''))
  await page.getByRole('button', { name: 'Done' }).click()
  await expect(page.getByTestId('person-e2e-streamer')).toContainText('Invited')

  await page
    .getByRole('navigation', { name: 'Main' })
    .getByRole('link', { name: 'Streams' })
    .click()
  await page.getByRole('button', { name: 'New stream' }).click()
  await page.getByLabel('Path').fill('e2e/streamer')
  await page.getByLabel('Title').fill('E2E streamer')
  await page.getByLabel('Owner').selectOption({ label: 'e2e-streamer (streamer, invited)' })
  await page.getByRole('button', { name: 'Create stream' }).click()
  await expect(page.getByRole('heading', { name: 'E2E streamer' })).toBeVisible()
  await expect(page.getByTestId('stream-state')).toHaveAttribute('data-state', 'offline')
})

test('stream settings save, and deleting a stream leaves its page', async ({ page }) => {
  await signInPage(page, '/streams')
  await page.getByRole('button', { name: 'New stream' }).click()
  await page.getByLabel('Path').fill('e2e/throwaway')
  await page.getByRole('button', { name: 'Create stream' }).click()
  await expect(page.getByRole('heading', { name: 'e2e/throwaway' })).toBeVisible()
  await expect(page.getByTestId('watch-link')).toBeVisible() // public by default

  // The form sends everything, the viewer limit (0, none) included.
  await page.getByLabel('Title').fill('Throwaway')
  await page.getByRole('checkbox', { name: /Public/ }).uncheck()
  await page.getByRole('button', { name: 'Save' }).click()
  await expect(page.getByText('Saved.')).toBeVisible()
  await expect(page.getByRole('heading', { name: 'Throwaway' })).toBeVisible()
  await expect(page.getByTestId('watch-link')).toHaveCount(0)

  await page.getByRole('button', { name: 'Delete stream' }).click()
  await page.getByRole('button', { name: 'Delete e2e/throwaway and its keys' }).click()
  await expect(page).toHaveURL(/\/streams$/)
  await expect(page.getByTestId('stream-e2e/throwaway')).toHaveCount(0)
  await expect(page.getByRole('alert')).toHaveCount(0)
})

async function joined(browser: Browser) {
  const context = await browser.newContext()
  const page = await context.newPage()
  await page.goto('/join')
  await page.getByLabel('Join code').fill(joinCode.toLowerCase())
  await page.getByLabel('New password').fill('streaming all night 42')
  await page.getByLabel('Password again').fill('streaming all night 42')
  await page.getByRole('button', { name: 'Join' }).click()
  await expect(page).toHaveURL(/\/streams$/)
  return { context, page }
}

test("the streamer joins, goes live with the page's key, and makes a new one", async ({
  browser,
}) => {
  test.setTimeout(150_000)
  const { context, page } = await joined(browser)
  const nav = page.getByRole('navigation', { name: 'Main' })
  await expect(nav.getByRole('link')).toHaveText(['Streams', 'Watch', 'Account'])
  await expect(page.getByTestId('stream-e2e/streamer')).toBeVisible()
  await expect(page.getByTestId('stream-e2e/other')).toHaveCount(0)

  // Nothing of anyone else's: not the other stream, not MediaMTX's API, not other paths in the event stream.
  const probe = await page.evaluate(async (other) => {
    const status = async (url: string) => (await fetch(url)).status
    const snapshot = await new Promise<string>((resolve) => {
      const es = new EventSource('/api/v1/events')
      es.addEventListener('snapshot', (e) => {
        es.close()
        resolve((e as MessageEvent<string>).data)
      })
    })
    return {
      other: await status(`/api/v1/streams/${String(other)}`),
      mtx: await status('/api/mtx/v3/paths/list'),
      dashboard: await status('/api/v1/status'),
      snapshot,
    }
  }, otherID)
  expect(probe.other).toBe(404)
  expect(probe.mtx).toBe(403)
  expect(probe.dashboard).toBe(403)
  expect(probe.snapshot).not.toContain('"name":"test"')
  expect(probe.snapshot).not.toContain('e2e/other')

  // Streamers only have the simple view: no mode switch.
  await expect(page.getByRole('group', { name: 'Mode' })).toHaveCount(0)
  // Watch offers their own streams only (the test publisher's "test" is live, but not theirs).
  await page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Watch' }).click()
  const tile = page.getByRole('combobox', { name: 'Stream for tile 1' })
  await expect(tile.locator('option')).toHaveText(['Empty', 'e2e/streamer (offline)'])
  await page
    .getByRole('navigation', { name: 'Main' })
    .getByRole('link', { name: 'Streams' })
    .click()
  await page.getByTestId('stream-e2e/streamer').click()
  await page.getByRole('button', { name: 'Show stream settings' }).click()
  await page.getByRole('button', { name: 'Show key' }).click()
  const server = (await page.getByTestId('golive-RTMP-Server').innerText()).trim()
  const key = (await page.getByTestId('golive-RTMP-Stream key').innerText()).trim()
  expect(server).toBe('rtmp://mediamtx/e2e')
  expect(key).toMatch(/^streamer\?user=key-[a-z2-7]{8}&pass=[a-z2-7]{32}$/)
  const secret = new URLSearchParams(key.split('?')[1]).get('pass') ?? ''
  recordSecret(secret)

  // OBS joins server and key with a slash; so does this.
  await rtmpPublish(`${server}/${key}`)
  await expect(page.getByTestId('stream-state')).toHaveAttribute('data-state', 'live', {
    timeout: 30_000,
  })
  await expect(page.getByTestId('stream-numbers')).toContainText('watching')
  await expect(page.getByTestId('player-mode')).toHaveAttribute('data-mode', 'webrtc', {
    timeout: 30_000,
  })

  // A target: its OBS settings, and the live stream checked against it (H.264 suits VRChat).
  await page.getByLabel('Where is the stream headed?').selectOption('vrchat-pc')
  await expect(page.getByRole('table', { name: 'OBS settings for VRChat (PC)' })).toBeVisible()
  await expect(page.getByTestId('health-codec')).toHaveAttribute('data-ok', 'true')
  await expect(page.getByTestId('health-resolution')).toBeVisible()

  // Forwarding to another server: MediaMTX takes it up while live (it cannot reach a documentation address, so it
  // keeps trying), switched off it stops, and the key never comes back or reaches a log.
  const fwdKey = 'e2e-forward-key-4f9c2a71' // gitleaks:allow (a test fixture)
  recordSecret(fwdKey)
  const forwarding = page.getByRole('region', { name: 'Forwarding' })
  await forwarding.getByRole('button', { name: 'Add a platform' }).click()
  await forwarding.getByRole('combobox', { name: /^Platform/ }).selectOption('custom')
  await forwarding.getByLabel(/^Server/).fill('rtmp://203.0.113.10/app')
  await forwarding.getByLabel(/^Stream key/).fill(fwdKey)
  await forwarding.getByRole('button', { name: 'Add', exact: true }).click()
  const fwd = forwarding.getByRole('list', { name: 'Forwards' }).getByRole('listitem')
  await expect(fwd).toContainText('rtmp://203.0.113.10')
  await expect(fwd.getByTestId('forward-state')).toHaveAttribute(
    'data-state',
    /^(forwarding|error)$/,
    { timeout: 15_000 },
  )
  await expect(page.locator('body')).not.toContainText(fwdKey)
  await fwd.getByRole('button', { name: 'Switch off' }).click()
  await expect(fwd.getByTestId('forward-state')).toHaveAttribute('data-state', 'off')
  await fwd.getByRole('button', { name: 'Remove Custom' }).click()
  await fwd.getByRole('button', { name: 'Remove', exact: true }).click()
  await expect(forwarding.getByRole('list', { name: 'Forwards' })).toHaveCount(0)

  // A new key: the encoder using the old one is disconnected, and the old one no longer works.
  await page.getByRole('button', { name: 'New key' }).click()
  await page.getByRole('button', { name: 'Make a new key' }).click()
  await expect(page.getByTestId('stream-state')).toHaveAttribute('data-state', 'offline', {
    timeout: 15_000,
  })
  const newKey = (await page.getByTestId('golive-RTMP-Stream key').innerText()).trim()
  expect(newKey).not.toBe(key)
  recordSecret(new URLSearchParams(newKey.split('?')[1]).get('pass') ?? '')
  expect(await rtmpTry(`${server}/${key}`), 'publishing with the old key').not.toBe(0)
  await expect(page.getByTestId('stream-state')).toHaveAttribute('data-state', 'offline')

  await rtmpPublish(`${server}/${newKey}`)
  await expect(page.getByTestId('stream-state')).toHaveAttribute('data-state', 'live', {
    timeout: 30_000,
  })

  // Public by default: the watch link plays for someone with no account at all.
  const link = (await page.getByTestId('watch-link').innerText()).trim()
  expect(link).toMatch(/\/s\/e2e\/streamer$/)
  const stranger = await browser.newContext()
  const guest = await stranger.newPage()
  await guest.goto(new URL(link).pathname)
  await expect(guest.getByTestId('public-state')).toHaveAttribute('data-state', 'live', {
    timeout: 15_000,
  })
  await expect(guest.getByTestId('player-mode')).toHaveAttribute('data-mode', 'webrtc', {
    timeout: 30_000,
  })
  expect((await guest.request.get('/api/v1/public/streams/e2e/other')).status()).toBe(404)
  await stranger.close()
  await page.getByRole('button', { name: 'Disconnect the encoder' }).click()
  await expect(page.getByTestId('stream-state')).toHaveAttribute('data-state', 'offline', {
    timeout: 15_000,
  })
  await context.close()
})

test('a guest streams with a guest key until it is revoked', async ({ page }) => {
  test.setTimeout(90_000)
  await signInPage(page, '/streams')
  await page.getByRole('button', { name: 'New stream' }).click()
  await page.getByLabel('Path').fill('e2e/guest')
  await page.getByRole('button', { name: 'Create stream' }).click()
  await expect(page.getByRole('heading', { name: 'e2e/guest' })).toBeVisible()

  const guests = page.getByRole('region', { name: 'Guest keys' })
  await guests.getByRole('button', { name: 'New guest key' }).click()
  await guests.getByRole('textbox', { name: 'For' }).fill('Bob (co-host)')
  await guests.getByRole('combobox', { name: /^Valid for/ }).selectOption('1')
  await guests.getByRole('button', { name: 'Make the key' }).click()
  const made = guests.getByTestId('guest-made')
  await expect(made).toContainText('Send these to Bob (co-host).')
  const server = (await made.getByTestId('guest-RTMP-Server').innerText()).trim()
  const key = (await made.getByTestId('guest-RTMP-Stream key').innerText()).trim()
  expect(key).toMatch(/^guest\?user=guest-[a-z2-7]{8}&pass=/)
  recordSecret(new URLSearchParams(key.split('?')[1]).get('pass') ?? '')
  await made.getByRole('button', { name: 'Done' }).click()
  await expect(guests.getByTestId('guest-made')).toHaveCount(0)
  await expect(page.locator('body')).not.toContainText(key)

  await rtmpPublish(`${server}/${key}`)
  await expect(page.getByTestId('stream-state')).toHaveAttribute('data-state', 'live', {
    timeout: 30_000,
  })

  // Revoking disconnects the guest at once, and the key no longer works.
  const row = guests.getByRole('list', { name: 'Valid guest keys' }).getByRole('listitem')
  await row.getByRole('button', { name: 'Revoke', exact: true }).click()
  await row.getByRole('button', { name: 'Revoke now' }).click()
  await expect(page.getByTestId('stream-state')).toHaveAttribute('data-state', 'offline', {
    timeout: 15_000,
  })
  await expect(guests.getByRole('list', { name: 'Valid guest keys' })).toHaveCount(0)
  expect(await rtmpTry(`${server}/${key}`), 'publishing with a revoked guest key').not.toBe(0)

  await page.getByRole('button', { name: 'Delete stream' }).click()
  await page.getByRole('button', { name: 'Delete e2e/guest and its keys' }).click()
  await expect(page).toHaveURL(/\/streams$/)
})

// A 1×1 PNG, for the holding screen's picture upload.
const png = Buffer.from(
  'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==',
  'base64',
)

test('a holding screen plays while nobody streams, and the encoder takes over', async ({
  page,
  browser,
}) => {
  test.setTimeout(120_000)
  await signInPage(page, '/streams')
  await page.getByRole('button', { name: 'New stream' }).click()
  await page.getByLabel('Path').fill('e2e/hold')
  await page.getByRole('button', { name: 'Create stream' }).click()
  await expect(page.getByRole('heading', { name: 'e2e/hold' })).toBeVisible()

  // The offline screen (the sidecar's clip in the stream's format, 1080p50 by default, with AAC): the preview and the
  // watch link play though nobody streams.
  const holding = page.getByRole('region', { name: 'Holding screen' })
  await holding.getByRole('radio', { name: /^Offline screen/ }).click()
  await expect(holding.getByRole('radio', { name: /^Offline screen/ })).toBeChecked({
    timeout: 15_000,
  })
  await expect(page.getByTestId('stream-state')).toHaveAttribute('data-state', 'offline')
  // WebRTC, not the HLS fallback: MediaMTX refuses WebRTC readers of H.264 with B-frames.
  await expect(page.getByTestId('player-mode')).toHaveAttribute('data-mode', 'webrtc', {
    timeout: 30_000,
  })
  const link = (await page.getByTestId('watch-link').innerText()).trim()
  const stranger = await browser.newContext()
  const viewer = await stranger.newPage()
  await viewer.goto(new URL(link).pathname)
  await expect(viewer.getByTestId('public-state')).toHaveAttribute('data-state', 'holding', {
    timeout: 15_000,
  })
  await expect(viewer.getByTestId('player-mode')).toHaveAttribute('data-mode', 'webrtc', {
    timeout: 30_000,
  })

  // An encoder with the clip's tracks (H.264, AAC 48 kHz stereo, as OBS sends over RTMP) takes over.
  await page.getByRole('button', { name: 'Show stream settings' }).click()
  await page.getByRole('button', { name: 'Show key' }).click()
  const server = (await page.getByTestId('golive-RTMP-Server').innerText()).trim()
  const key = (await page.getByTestId('golive-RTMP-Stream key').innerText()).trim()
  recordSecret(new URLSearchParams(key.split('?')[1]).get('pass') ?? '')
  await rtmpPublish(`${server}/${key}`)
  await expect(page.getByTestId('stream-state')).toHaveAttribute('data-state', 'live', {
    timeout: 30_000,
  })
  await expect(viewer.getByTestId('public-state')).toHaveAttribute('data-state', 'live', {
    timeout: 15_000,
  })
  // A holding change now would cut off the encoder and the viewer: the page asks first.
  await holding.getByRole('combobox', { name: /^Format/ }).selectOption('720p50')
  const confirm = holding.getByTestId('holding-confirm')
  await expect(confirm).toContainText('your encoder is disconnected and reconnects')
  await expect(confirm).toContainText('1 person watching')
  await confirm.getByRole('button', { name: 'Cancel' }).click()
  await expect(confirm).toHaveCount(0)
  await expect(holding.getByRole('combobox', { name: /^Format/ })).toHaveValue('1080p50')
  await expect(page.getByTestId('stream-state')).toHaveAttribute('data-state', 'live')
  await page.getByRole('button', { name: 'Disconnect the encoder' }).click()
  await expect(viewer.getByTestId('public-state')).toHaveAttribute('data-state', 'holding', {
    timeout: 15_000,
  })
  await stranger.close()

  // A picture becomes a clip in the browser (Opus: Linux browsers cannot encode AAC), when it can encode.
  const canEncode = await page.evaluate(async () => {
    if (typeof VideoEncoder === 'undefined' || typeof AudioEncoder === 'undefined') return false
    const v = await VideoEncoder.isConfigSupported({
      codec: 'avc1.640028',
      width: 1920,
      height: 1080,
    })
    const a = await AudioEncoder.isConfigSupported({
      codec: 'opus',
      sampleRate: 48000,
      numberOfChannels: 2,
    })
    return Boolean(v.supported && a.supported)
  })
  // Other audio and format: the offline screen follows at once.
  // (A viewer that just left can still count for a few seconds; then the page asks, and this confirms.)
  const confirmed = async (expectation: () => Promise<void>) => {
    await expect(async () => {
      if (await confirm.isVisible()) {
        await confirm.getByRole('button', { name: 'Change it anyway' }).click()
      }
      await expectation()
    }).toPass({ timeout: 15_000 })
  }
  const audioSelect = holding.getByRole('combobox', { name: /^Your encoder sends audio as/ })
  await audioSelect.selectOption('opus')
  await confirmed(() => expect(audioSelect).toHaveValue('opus', { timeout: 1000 }))
  await holding.getByRole('combobox', { name: /^Format/ }).selectOption('720p60')
  await confirmed(() =>
    expect(holding.getByRole('combobox', { name: /^Format/ })).toHaveValue('720p60', {
      timeout: 1000,
    }),
  )
  await expect(holding.getByRole('radio', { name: /^Offline screen/ })).toBeChecked()
  await expect(holding.getByRole('alert')).toHaveCount(0)
  // The path restarted with the new clip: the preview reconnects by itself and shows it.
  await expect(page.getByTestId('player-stats')).toContainText('1280×720', { timeout: 20_000 })
  await expect(page.getByTestId('player-mode')).toHaveAttribute('data-mode', 'webrtc')

  // An encoder whose tracks do not match (video only, while the clip has Opus) is refused, and the page says why.
  expect(
    await rtmpTry(`${server}/${key}`),
    "publishing without the holding clip's tracks",
  ).not.toBe(0)
  await expect(page.getByTestId('encoder-notes')).toContainText('Your encoder was refused', {
    timeout: 15_000,
  })
  await expect(page.getByTestId('encoder-notes')).toContainText('expects Opus + H264')

  // An encoder with AAC (RTMP, as OBS sends): refused once, the holding screen follows its audio, the next attempt
  // (OBS reconnects by itself) gets in.
  expect(await rtmpTry(`${server}/${key}`, true), 'the first AAC attempt').not.toBe(0)
  await expect(page.getByTestId('encoder-notes')).toContainText('Holding screen switched', {
    timeout: 15_000,
  })
  await expect(audioSelect).toHaveValue('aac')
  await rtmpPublish(`${server}/${key}`)
  await expect(page.getByTestId('stream-state')).toHaveAttribute('data-state', 'live', {
    timeout: 30_000,
  })
  await page.getByRole('button', { name: 'Disconnect the encoder' }).click()
  await expect(page.getByTestId('stream-state')).toHaveAttribute('data-state', 'offline', {
    timeout: 15_000,
  })
  if (canEncode) {
    await holding
      .getByLabel('Picture or video for the holding screen')
      .setInputFiles({ name: 'brb.png', mimeType: 'image/png', buffer: png })
    await expect(holding.getByRole('radio', { name: 'My own clip' })).toBeChecked({
      timeout: 60_000,
    })
    await expect(holding.getByRole('alert')).toHaveCount(0)
  } else {
    test.info().annotations.push({ type: 'skipped part', description: 'no H.264/Opus encoder' })
  }

  await page.getByRole('button', { name: 'Delete stream' }).click()
  await page.getByRole('button', { name: 'Delete e2e/hold and its keys' }).click()
  await expect(page).toHaveURL(/\/streams$/)
})

test('an encoder with B-frames plays over HLS, and the page says why', async ({ page }) => {
  test.setTimeout(90_000)
  await signInPage(page, '/streams')
  await page.getByRole('button', { name: 'New stream' }).click()
  await page.getByLabel('Path').fill('e2e/bframes')
  await page.getByRole('button', { name: 'Create stream' }).click()
  await expect(page.getByRole('heading', { name: 'e2e/bframes' })).toBeVisible()
  await page.getByRole('button', { name: 'Show stream settings' }).click()
  await page.getByRole('button', { name: 'Show key' }).click()
  const server = (await page.getByTestId('golive-RTMP-Server').innerText()).trim()
  const key = (await page.getByTestId('golive-RTMP-Stream key').innerText()).trim()
  recordSecret(new URLSearchParams(key.split('?')[1]).get('pass') ?? '')

  // MediaMTX closes WebRTC readers of H.264 with B-frames: the preview falls back to HLS with the reason, and the
  // page explains it from MediaMTX's log.
  await rtmpPublish(`${server}/${key}`, true)
  await expect(page.getByTestId('stream-state')).toHaveAttribute('data-state', 'live', {
    timeout: 30_000,
  })
  await expect(page.getByTestId('player-mode')).toHaveAttribute('data-mode', 'hls', {
    timeout: 30_000,
  })
  await expect(page.getByTestId('player-note')).toContainText('B-frames')
  await expect(page.getByTestId('encoder-notes')).toContainText('B-frames from your encoder', {
    timeout: 15_000,
  })

  await page.getByRole('button', { name: 'Disconnect the encoder' }).click()
  await expect(page.getByTestId('stream-state')).toHaveAttribute('data-state', 'offline', {
    timeout: 15_000,
  })
  await page.getByRole('button', { name: 'Delete stream' }).click()
  await page.getByRole('button', { name: 'Delete e2e/bframes and its keys' }).click()
  await expect(page).toHaveURL(/\/streams$/)
})
