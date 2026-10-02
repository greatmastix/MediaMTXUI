import { baseURL, control, expect, sameOrigin, signIn, signInPage, test } from './fixtures'

// Recordings against the real stack: the test publisher's path records in 2 s segments onto a
// 64 MB tmpfs with a 4 MiB budget. The timeline shows the segments, a stretch plays and downloads (its duration within
// one segment), deleting removes the segment from the listing, the budget keeps usage under it, and filling the volume
// with something else trips the guard, which an admin releases once there is room.

interface Listing {
  disk: { recordings: number; budget: number; guard: { paths: string[] | null } | null }
  paths: { name: string; segments: number }[]
}

/** An MP4's duration in seconds, from its mvhd box. */
function mp4Duration(buf: Buffer): number {
  const i = buf.indexOf('mvhd')
  if (i < 0) throw new Error('no mvhd box')
  const v1 = buf[i + 4] === 1
  const timescale = buf.readUInt32BE(i + (v1 ? 24 : 16))
  const duration = v1 ? Number(buf.readBigUInt64BE(i + 28)) : buf.readUInt32BE(i + 20)
  return duration / timescale
}

test('recordings: timeline, playback, export, delete, budget and guard', async ({
  page,
  playwright,
}) => {
  test.setTimeout(240_000)
  const api = await playwright.request.newContext({ baseURL })
  const headers = { ...sameOrigin, 'X-CSRF-Token': await signIn(api) }
  const listing = async () => (await (await api.get('/api/v1/recordings')).json()) as Listing
  const starts = async () =>
    ((await (await api.get('/api/v1/recordings/segments?path=test')).json()) as { start: string }[])
      .map((s) => s.start)
      .sort()

  const on = await api.put('/api/v1/config/paths/test', {
    headers,
    data: { config: { record: true, recordSegmentDuration: '2s' } },
  })
  expect(on.status()).toBe(200)
  await expect
    .poll(async () => (await starts()).length, { timeout: 60_000, intervals: [1000] })
    .toBeGreaterThanOrEqual(4)

  // An exported stretch lasts what was asked, within one segment.
  const all = await starts()
  const from = all[all.length - 3] ?? ''
  const exp = await api.get(
    `/api/v1/recordings/export?${new URLSearchParams({ path: 'test', start: from, duration: '2', format: 'mp4' }).toString()}`,
  )
  expect(exp.status()).toBe(200)
  expect(Math.abs(mp4Duration(await exp.body()) - 2)).toBeLessThanOrEqual(2)

  // The timeline: latest, zoomed in, a span selected with a double click, played here.
  await signInPage(page, '/recordings')
  await page.getByTestId('recording-row-test').getByRole('link', { name: 'test' }).click()
  await page.getByRole('button', { name: 'Latest' }).click()
  await page.getByRole('group', { name: 'Zoom' }).getByRole('button', { name: '10 min' }).click()
  const span = page.getByTestId('timeline-span').last()
  await expect(span).toBeVisible({ timeout: 15_000 })
  const box = await span.boundingBox()
  if (!box) throw new Error('no span on the timeline')
  await page.mouse.dblclick(box.x + box.width / 2, box.y + box.height / 2)
  await expect(page.getByTestId('selection-summary')).toContainText('segment')
  await page.getByRole('button', { name: 'Play' }).click()
  const src = await page.getByTestId('recording-player').getAttribute('src')
  expect(src).toContain('format=fmp4')
  const played = await page.request.get(src ?? '')
  expect(played.status()).toBe(200)
  expect((await played.body()).subarray(4, 8).toString()).toBe('ftyp')

  // Delete one segment (a recent one, so the budget does not take it first): it leaves the listing.
  const victim = (await starts()).at(-2) ?? ''
  const t = new Date(victim)
  const local = new Date(t.getTime() - t.getTimezoneOffset() * 60_000).toISOString().slice(0, 19)
  await page.getByLabel('From').fill(local)
  await page.getByLabel('Length (seconds)').fill('1')
  await page.getByRole('button', { name: 'Delete 1 segment' }).click()
  await page
    .getByRole('alertdialog', { name: 'Delete recordings' })
    .getByRole('button', { name: 'Delete' })
    .click()
  await expect(page.getByText('Deleted 1 segment')).toBeVisible()
  expect(await starts()).not.toContain(victim)

  // The budget: the oldest segments go, and usage stays under it (give or take the segment being written).
  const oldest = (await starts())[0] ?? ''
  await expect.poll(async () => (await starts()).includes(oldest), { timeout: 60_000 }).toBe(false)
  const { disk } = await listing()
  expect(disk.recordings).toBeLessThanOrEqual(disk.budget + 1024 * 1024)
  expect(disk.recordings).toBeGreaterThan(disk.budget / 2) // only what is needed goes

  // Something else fills the volume: pruning cannot make room, the guard switches recording off.
  await control('recordings-fill')
  try {
    await expect
      .poll(async () => (await listing()).disk.guard?.paths ?? [], { timeout: 30_000 })
      .toContain('test')
    await page.goto('/recordings')
    await expect(page.getByTestId('recordings-guard')).toBeVisible()
    await expect(page.getByTestId('warning-recordings_guard')).toBeVisible()
    const cfg = (await (await api.get('/api/v1/config')).json()) as { content: string }
    expect(cfg.content).toMatch(/ {2}test:\n(?: {4}.*\n)*? {4}record: no/)
  } finally {
    await control('recordings-unfill')
  }
  await page.getByRole('button', { name: 'Switch recording back on' }).click()
  await expect(page.getByTestId('recordings-guard')).toHaveCount(0)
  expect((await listing()).disk.guard).toBeNull()
  const after = (await (await api.get('/api/v1/config')).json()) as { content: string }
  expect(after.content).toMatch(/ {2}test:\n(?: {4}.*\n)*? {4}record: yes/)
})
