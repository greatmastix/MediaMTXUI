import { baseURL, expect, recordSecret, sameOrigin, signIn, test } from './fixtures'

// WHIP and WHEP for clients outside the UI, with real WebRTC in Chromium: a stream published over /whip/<path> with a
// publish credential goes live in MediaMTX, and a reader with a token credential receives it over /whep/<path>. No
// cookie takes part (credentials: 'omit'), as with OBS or another server. The path must not have a segment named whip
// or whep: MediaMTX then cannot parse its own session URLs.

test('publish over WHIP and read over WHEP with stream credentials', async ({
  page,
  playwright,
}) => {
  test.setTimeout(90_000)
  const request = await playwright.request.newContext({ baseURL })
  const csrf = await signIn(request)
  const headers = { ...sameOrigin, 'X-CSRF-Token': csrf }
  const create = async (data: object) => {
    const res = await request.post('/api/v1/credentials', { headers, data })
    expect(res.status(), await res.text()).toBe(201)
    const { secret } = (await res.json()) as { secret: string }
    recordSecret(secret)
    return secret
  }
  const pub = await create({ name: 'e2e-whip', actions: ['publish'], paths: ['e2e/obs'] })
  const token = await create({
    name: 'e2e-whep',
    kind: 'token',
    actions: ['read'],
    paths: ['e2e/obs'],
  })

  await page.goto('/login') // any page of the origin; no one is signed in
  const published = await page.evaluate(
    async (auth) => {
      const canvas = document.createElement('canvas')
      canvas.width = 320
      canvas.height = 240
      const g = canvas.getContext('2d')
      let n = 0
      setInterval(() => {
        if (!g) return
        g.fillStyle = `hsl(${String((n += 7) % 360)} 80% 50%)`
        g.fillRect(0, 0, 320, 240)
      }, 33)
      const stream = canvas.captureStream(30)
      const pc = new RTCPeerConnection()
      for (const t of stream.getTracks()) pc.addTransceiver(t, { direction: 'sendonly' })
      await pc.setLocalDescription(await pc.createOffer())
      await new Promise((r) => setTimeout(r, 1000))
      const res = await fetch('/whip/e2e/obs', {
        method: 'POST',
        headers: { 'Content-Type': 'application/sdp', Authorization: auth },
        body: pc.localDescription?.sdp,
        credentials: 'omit',
      })
      if (res.status !== 201) return `offer ${String(res.status)}`
      await pc.setRemoteDescription({ type: 'answer', sdp: await res.text() })
      ;(window as unknown as { pub: RTCPeerConnection }).pub = pc
      return 'ok'
    },
    `Basic ${Buffer.from(`e2e-whip:${pub}`).toString('base64')}`,
  )
  expect(published).toBe('ok')
  await expect
    .poll(
      async () =>
        (
          (await (await request.get('/api/mtx/v3/paths/get/e2e/obs')).json()) as {
            online?: boolean
          }
        ).online,
      {
        timeout: 20_000,
      },
    )
    .toBe(true)

  const reader = await page.context().newPage()
  await reader.goto('/login')
  const read = async (auth: string) =>
    reader.evaluate(async (a) => {
      const pc = new RTCPeerConnection()
      pc.addTransceiver('video', { direction: 'recvonly' })
      const track = new Promise<string>((r) => (pc.ontrack = (e) => r(e.track.kind)))
      await pc.setLocalDescription(await pc.createOffer())
      await new Promise((r) => setTimeout(r, 1000))
      const res = await fetch('/whep/e2e/obs', {
        method: 'POST',
        headers: { 'Content-Type': 'application/sdp', Authorization: a },
        body: pc.localDescription?.sdp,
        credentials: 'omit',
      })
      if (res.status !== 201) return `offer ${String(res.status)}`
      await pc.setRemoteDescription({ type: 'answer', sdp: await res.text() })
      const kind = await Promise.race([
        track,
        new Promise<string>((r) => setTimeout(() => r('no track'), 15_000)),
      ])
      const del = await fetch(res.headers.get('Location') ?? '', {
        method: 'DELETE',
        headers: { Authorization: a },
        credentials: 'omit',
      })
      pc.close()
      return `${kind} delete ${String(del.status)}${del.status === 200 ? '' : ' ' + (await del.text())}`
    }, auth)
  expect(await read('Bearer wrong-token-000000000000')).toBe('offer 401')
  expect(await read(`Bearer ${token}`)).toBe('video delete 200')
  await request.dispose()
})
