import { baseURL, control, expect, sameOrigin, signIn, signInPage, test } from './fixtures'

// Config editing against the real stack: MediaMTX validates and applies what the sidecar writes, and an invalid edit
// made outside the sidecar is reverted before MediaMTX can stay down.

test.describe.configure({ mode: 'serial' })

test('a path saved through the API is applied by MediaMTX, and removed again', async ({
  playwright,
}) => {
  const request = await playwright.request.newContext({ baseURL })
  const csrf = await signIn(request)
  const headers = { ...sameOrigin, 'X-CSRF-Token': csrf }

  const before = (await (await request.get('/api/v1/config')).json()) as { content: string }
  let res = await request.put('/api/v1/config/paths/e2e/cam', {
    headers,
    data: { config: { source: 'publisher', maxReaders: 3 } },
  })
  expect(res.status(), await res.text()).toBe(200)
  const after = (await (await request.get('/api/v1/config')).json()) as { content: string }
  expect(after.content.replace('  e2e/cam:\n    maxReaders: 3\n    source: publisher\n', '')).toBe(
    before.content,
  )

  // MediaMTX reloads the file and knows the path.
  await expect
    .poll(async () => (await request.get('/api/mtx/v3/paths/get/e2e/cam')).status(), {
      timeout: 10_000,
    })
    .not.toBe(404)

  // MediaMTX's own validation refuses what it would not load; nothing is written.
  res = await request.put('/api/v1/config/paths/e2e/cam', {
    headers,
    data: { config: { maxReaders: 'many' } },
  })
  expect(res.status()).toBe(422)
  expect(((await res.json()) as { message: string }).message).toContain(
    'MediaMTX rejects the config',
  )

  res = await request.delete('/api/v1/config/paths/e2e/cam', { headers })
  expect(res.status(), await res.text()).toBe(200)
  const restored = (await (await request.get('/api/v1/config')).json()) as { content: string }
  expect(restored.content).toBe(before.content)
  await request.dispose()
})

test('an invalid edit outside the sidecar is reverted and MediaMTX is back within 5 s', async ({
  page,
  playwright,
}) => {
  test.setTimeout(90_000)
  const request = await playwright.request.newContext({ baseURL })
  const csrf = await signIn(request)
  const good = (await (await request.get('/api/v1/config')).json()) as { content: string }
  await signInPage(page)
  await expect(page.getByTestId('live-indicator')).toHaveAttribute('data-state', 'live')

  // MediaMTX ignores file changes within a second of its last reload; the tests before this one just wrote.
  await page.waitForTimeout(1500)
  const broken = await control('config-break')
  // MediaMTX exits on the invalid reload; the sidecar restores the file within its 2 s check and Docker restarts
  // MediaMTX on it. Back means its API answers the sidecar again.
  await expect
    .poll(
      async () =>
        ((await (await request.get('/api/v1/config')).json()) as { content: string }).content,
      {
        timeout: 10_000,
        intervals: [100],
      },
    )
    .toBe(good.content)
  await expect
    .poll(async () => (await request.get('/api/mtx/v3/info')).status(), {
      timeout: 10_000,
      intervals: [100],
    })
    .toBe(200)
  const backAfter = Date.now() - broken
  console.log(`invalid outside edit: MediaMTX back after ${backAfter} ms`)
  expect(backAfter).toBeLessThan(5000)

  // Every page says what happened until an admin dismisses it; the audit log keeps the rejected file. (A reload
  // rather than waiting for the status poll: the warning is what is tested, not the poll's interval.)
  await page.reload()
  await expect(page.getByTestId('warning-config_drift')).toContainText('invalid config')
  const res = await request.post('/api/v1/config/drift/dismiss', {
    headers: { ...sameOrigin, 'X-CSRF-Token': csrf },
  })
  expect(res.status()).toBe(204)
  await expect(page.getByTestId('live-indicator')).toHaveAttribute('data-state', 'live', {
    timeout: 30_000,
  })
  await request.dispose()
})

test('the config UI: a path, a global setting, the YAML check and the history', async ({
  page,
}) => {
  test.setTimeout(90_000)
  await signInPage(page, '/config/paths')
  await page.getByRole('link', { name: 'New path' }).click()
  await page.getByLabel('name', { exact: true }).fill('ui/cam')
  await page.getByLabel('source', { exact: true }).selectOption('url')
  await page.getByRole('textbox', { name: 'Source URL' }).fill('rtsp://127.0.0.1:9997/')
  await page.getByRole('button', { name: 'Create path' }).click()
  // The SSRF guard answers in the page. (Alerts are picked by their text: the "MediaMTX is not answering" banner can
  // show next to them while a setting change restarts MediaMTX's API.)
  await expect(page.getByRole('alert').filter({ hasText: 'loopback' })).toBeVisible()
  await page.getByRole('textbox', { name: 'Source URL' }).fill('rtsp://192.0.2.10:554/stream')
  await page.getByLabel('sourceOnDemand', { exact: true }).selectOption('yes')
  await page.getByRole('button', { name: 'Create path' }).click()
  await expect(page).toHaveURL(/\/config\/paths\/ui\/cam$/)
  await expect(page.getByTestId('saved-note')).toContainText('MediaMTX applied it')

  await page
    .getByRole('navigation', { name: 'Configuration' })
    .getByRole('link', { name: 'Global settings' })
    .click()
  await page.getByLabel('readTimeout', { exact: true }).fill('15s')
  await page.getByRole('button', { name: 'Save' }).click()
  await expect(page.getByTestId('saved-note')).toContainText('Saved as version')
  await expect(page.getByLabel('readTimeout', { exact: true })).toHaveValue('15s')

  await page
    .getByRole('navigation', { name: 'Configuration' })
    .getByRole('link', { name: 'YAML' })
    .click()
  const yaml = page.getByRole('textbox', { name: 'mediamtx.yml' })
  await expect(yaml).toHaveValue(/readTimeout: 15s/)
  await expect(yaml).toHaveValue(
    / {2}ui\/cam:\n {4}source: rtsp:\/\/192\.0\.2\.10:554\/stream\n {4}sourceOnDemand: yes\n/,
  )
  await page.getByRole('button', { name: 'Check' }).click()
  await expect(page.getByTestId('yaml-valid')).toBeVisible()
  const text = await yaml.inputValue()
  await yaml.fill(text.replace('rtspTransports: [tcp]', 'rtspTransports: [bogus]'))
  await page.getByRole('button', { name: 'Check' }).click()
  await expect(
    page.getByRole('alert').filter({ hasText: 'MediaMTX rejects the config' }),
  ).toBeVisible()

  await page
    .getByRole('navigation', { name: 'Configuration' })
    .getByRole('link', { name: 'History' })
    .click()
  await expect(page.getByTestId('diff')).toContainText('readTimeout: 15s')
  // Back to the version before the path: the path is gone again.
  const versions = page.getByRole('list', { name: 'Versions' }).getByRole('link')
  await versions.nth(2).click()
  await page.getByRole('button', { name: 'Restore…' }).click()
  await page.getByRole('button', { name: /^Restore version/ }).click()
  // Two verified writes: ui/cam falls to all_others, so the writer closes it in MediaMTX first (mtxconf/closing.go).
  await expect(page.getByTestId('saved-note')).toContainText('Restored as version', {
    timeout: 20_000,
  })
  await page
    .getByRole('navigation', { name: 'Configuration' })
    .getByRole('link', { name: 'Paths' })
    .click()
  await expect(page.getByTestId('config-path-ui/cam')).toHaveCount(0)
})

/** The RTSP session of the test publisher, as MediaMTX reports it. */
async function publisherSession(request: import('@playwright/test').APIRequestContext) {
  const res = await request.get('/api/mtx/v3/rtsp/sessions/list')
  const list = (await res.json()) as { items: { id: string; path: string; state: string }[] }
  return list.items.find((s) => s.path === 'test' && s.state === 'publish')?.id
}

test('every source type is accepted by MediaMTX', async ({ playwright }) => {
  test.setTimeout(90_000)
  const request = await playwright.request.newContext({ baseURL })
  const csrf = await signIn(request)
  const headers = { ...sameOrigin, 'X-CSRF-Token': csrf }
  const before = (await (await request.get('/api/v1/config')).json()) as {
    content: string
    sha256: string
  }
  const sources: Record<string, string | Record<string, unknown>> = {
    publisher: 'publisher',
    rtsp: 'rtsp://user:pass@192.0.2.10:554/stream',
    rtsps: 'rtsps://192.0.2.10:322/stream',
    rtspHttp: 'rtsp+http://192.0.2.10:80/stream',
    rtspsHttp: 'rtsps+http://192.0.2.10:443/stream',
    rtspWs: 'rtsp+ws://192.0.2.10:80/stream',
    rtspsWs: 'rtsps+ws://192.0.2.10:443/stream',
    rtmp: 'rtmp://192.0.2.10/app/stream',
    rtmps: 'rtmps://192.0.2.10/app/stream',
    hls: 'http://192.0.2.10/stream/index.m3u8',
    hlss: 'https://192.0.2.10/stream/index.m3u8',
    udpMpegts: 'udp+mpegts://238.0.0.1:1234',
    udpRtp: {
      source: 'udp+rtp://238.0.0.1:5004',
      rtpSDP:
        'v=0\r\no=- 0 0 IN IP4 127.0.0.1\r\ns=-\r\nc=IN IP4 238.0.0.1\r\nt=0 0\r\n' +
        'm=video 5004 RTP/AVP 96\r\na=rtpmap:96 H264/90000\r\n',
    },
    srt: 'srt://192.0.2.10:8890?streamid=read:stream',
    moq: 'moqt://192.0.2.10:4443/stream',
    whep: 'whep://192.0.2.10:8889/stream/whep',
    wheps: 'wheps://192.0.2.10:8889/stream/whep',
    redirect: { source: 'redirect', sourceRedirect: 'rtsp://192.0.2.10/other' },
  }
  const block = Object.entries(sources)
    .map(([name, s]) => {
      const conf =
        typeof s === 'string'
          ? { source: s, sourceOnDemand: s !== 'publisher' && s !== 'redirect' }
          : s
      const lines = Object.entries(conf).map(
        ([k, v]) => `    ${k}: ${typeof v === 'boolean' ? (v ? 'yes' : 'no') : JSON.stringify(v)}`,
      )
      return `  types/${name}:\n${lines.join('\n')}\n`
    })
    .join('')
  expect(before.content).toContain('\npaths:\n')
  const content = before.content.replace('\npaths:\n', `\npaths:\n${block}`)
  let res = await request.put('/api/v1/config', {
    headers,
    data: { content, sha256: before.sha256, reason: 'every source type' },
  })
  expect(res.status(), await res.text()).toBe(200)
  const written = (await res.json()) as { sha256: string; applied?: { state: string } }
  // The post-apply check read every new path back from MediaMTX.
  expect(written.applied?.state).toBe('verified')

  res = await request.put('/api/v1/config', {
    headers,
    data: { content: before.content, sha256: written.sha256, reason: 'back' },
  })
  expect(res.status(), await res.text()).toBe(200)
  await request.dispose()
})

test('a refused config and an HLS change leave the RTSP publisher connected', async ({
  playwright,
}) => {
  test.setTimeout(90_000)
  const request = await playwright.request.newContext({ baseURL })
  const csrf = await signIn(request)
  const headers = { ...sameOrigin, 'X-CSRF-Token': csrf }
  await expect.poll(() => publisherSession(request), { timeout: 60_000 }).toBeTruthy()
  const session = await publisherSession(request)

  // Refused before anything is written: MediaMTX never sees it.
  let res = await request.patch('/api/v1/config/global', {
    headers,
    data: { set: { rtspTransports: ['bogus'] } },
  })
  expect(res.status()).toBe(422)

  // Only the HLS server restarts.
  res = await request.patch('/api/v1/config/global', {
    headers,
    data: { set: { hlsSegmentCount: 9 } },
  })
  expect(res.status(), await res.text()).toBe(200)
  expect(((await res.json()) as { applied?: { state: string } }).applied?.state).toBe('verified')
  expect(await publisherSession(request)).toBe(session)

  res = await request.patch('/api/v1/config/global', {
    headers,
    data: { remove: ['hlsSegmentCount'] },
  })
  expect(res.status(), await res.text()).toBe(200)
  expect(await publisherSession(request)).toBe(session)
  await request.dispose()
})

test('quick setup re-streams a camera in one step', async ({ page, playwright }) => {
  test.setTimeout(60_000)
  await signInPage(page, '/config')
  await expect(page).toHaveURL(/\/config\/quick$/)
  await page.getByRole('button', { name: /Re-stream a camera/ }).click()
  await page.getByLabel('Path name').fill('quick/cam')
  await page.getByLabel('Pull from').fill('rtsp://192.0.2.30:554/stream1')
  await page.getByRole('button', { name: 'Save' }).click()
  const done = page.getByTestId('quick-done')
  await expect(done).toContainText('quick/cam is set up.')
  await expect(done).toContainText('rtsp://mediamtx:8554/quick/cam')

  const request = await playwright.request.newContext({ baseURL })
  const csrf = await signIn(request)
  // MediaMTX knows the path; then it goes again.
  expect((await request.get('/api/mtx/v3/paths/get/quick/cam')).status()).not.toBe(404)
  const res = await request.delete('/api/v1/config/paths/quick/cam', {
    headers: { ...sameOrigin, 'X-CSRF-Token': csrf },
  })
  expect(res.status(), await res.text()).toBe(200)
  await request.dispose()
})
