import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { createMemoryHistory, RouterProvider } from '@tanstack/react-router'
import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'

import { setCsrfToken } from '@/api/client'
import type { Session } from '@/api/sidecar'
import type { Stream } from '@/api/streams'
import { createAppRouter } from '@/router'
import { installFakeEventSource } from '@/test/fakeEventSource'
import { adminSession, fakeSidecar, json } from '@/test/fakeSidecar'

function renderApp(path: string) {
  setCsrfToken(adminSession.csrfToken)
  installFakeEventSource()
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const router = createAppRouter(queryClient, createMemoryHistory({ initialEntries: [path] }))
  render(
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  )
  return queryClient
}

const streamer: Session = { ...adminSession, user: { id: 7, username: 'alice', role: 'streamer' } }

const alice = (): Stream => ({
  id: 1,
  name: 'live/alice',
  title: 'Alice',
  target: '',
  maxReaders: 0,
  record: true,
  public: true,
  owner: { id: 7, username: 'alice' },
  keys: {},
  createdAt: '2026-09-29T10:00:00Z',
  createdBy: 'admin',
  canManage: true,
  canAdmin: false,
  ingest: { host: 'mtx.example.com', rtmp: 1935, hls: true, webrtc: true },
  audio: 'aac',
  holding: '',
  clips: { aac: true, opus: true },
  format: '720p50',
})

/** The fake sidecar with one stream, which a PATCH changes as asked. */
function withStream(session: Session, extra: Parameters<typeof fakeSidecar>[1] = {}) {
  const stream = alice()
  const fake = fakeSidecar(
    { session },
    {
      ...extra,
      'GET /api/v1/streams/1': () => json(200, stream),
      'PATCH /api/v1/streams/1': (body) => {
        Object.assign(stream, body)
        return json(200, stream)
      },
    },
  )
  return { ...fake, stream }
}

describe('stream settings', () => {
  it('sends only what was changed, and follows changes made elsewhere meanwhile', async () => {
    const { calls, stream } = withStream(streamer)
    const queryClient = renderApp('/streams/1')
    const settings = await screen.findByRole('region', { name: 'Settings' })
    const isPublic = within(settings).getByRole('checkbox', { name: /^Public/ })
    const record = within(settings).getByRole('checkbox', { name: /^Record this stream/ })
    const save = within(settings).getByRole('button', { name: 'Save' })
    expect(isPublic).toBeChecked()
    expect(record).toBeChecked()
    expect(save).toBeDisabled() // nothing changed yet

    // Meanwhile an operator makes the stream private, and the recordings guard stops its recording.
    stream.public = false
    stream.record = false
    await act(() => queryClient.invalidateQueries({ queryKey: ['streams', 1] }))
    await waitFor(() => {
      expect(isPublic).not.toBeChecked()
    })
    expect(record).not.toBeChecked()

    // A new title goes alone: neither is undone.
    const user = userEvent.setup()
    const title = within(settings).getByRole('textbox', { name: 'Title' })
    await user.clear(title)
    await user.type(title, 'Alice live')
    await user.click(save)
    expect(await within(settings).findByText('Saved.')).toBeInTheDocument()
    const patches = () => calls.filter((c) => c.method === 'PATCH').map((c) => c.body)
    expect(patches()).toEqual([{ title: 'Alice live' }])
    expect(stream).toMatchObject({ title: 'Alice live', public: false, record: false })
    expect(title).toHaveValue('Alice live')
    expect(save).toBeDisabled()

    // What is changed on purpose goes, and only that.
    await user.click(isPublic)
    await user.click(save)
    await waitFor(() => {
      expect(patches()).toHaveLength(2)
    })
    expect(patches()[1]).toEqual({ public: true })

    // Changing a field and back again leaves nothing to send.
    await user.click(record)
    await user.click(record)
    expect(save).toBeDisabled()
  })

  it('never sends the owner back when another admin moved the stream meanwhile', async () => {
    const { calls, stream } = withStream(adminSession)
    stream.canAdmin = true
    const queryClient = renderApp('/streams/1')
    const settings = await screen.findByRole('region', { name: 'Settings' })
    stream.owner = { id: 8, username: 'bob' }
    await act(() => queryClient.invalidateQueries({ queryKey: ['streams', 1] }))
    await waitFor(() => {
      expect(screen.getByText(/streamed by bob/)).toBeInTheDocument()
    })
    const user = userEvent.setup()
    await user.type(within(settings).getByRole('spinbutton', { name: /Viewer limit/ }), '5')
    await user.click(within(settings).getByRole('button', { name: 'Save' }))
    await waitFor(() => {
      expect(calls.some((c) => c.method === 'PATCH')).toBe(true)
    })
    expect(calls.find((c) => c.method === 'PATCH')?.body).toEqual({ maxReaders: 5 })
    expect(stream.owner).toEqual({ id: 8, username: 'bob' })
  })
})

describe('holding screen from a recording', () => {
  it('offers the stretch the recording page sent, fetches it to make the clip, and can be turned down', async () => {
    const { calls } = withStream(streamer, {
      'GET /api/v1/recordings/export': () =>
        new Response(new Uint8Array([0, 0, 0, 8, 102, 114, 101, 101]), {
          headers: { 'Content-Type': 'video/mp4' },
        }),
    })
    renderApp('/streams/1?clipFrom=2026-10-03T09%3A00%3A00.000Z&clipSeconds=90')
    const offer = await screen.findByRole('group', { name: 'Stretch from the recording' })
    expect(offer).toHaveTextContent('(1m 30s)')
    expect(offer).toHaveTextContent('It is transcoded here into 720p50 with AAC')
    const user = userEvent.setup()
    await user.click(within(offer).getByRole('button', { name: 'Make it the holding screen' }))
    // The stretch is fetched as Download MP4 would; this browser (jsdom) has no encoder, which the card says.
    const holding = screen.getByRole('region', { name: 'Holding screen' })
    expect(await within(holding).findByRole('alert')).toHaveTextContent('cannot encode')
    const fetched = calls.find((c) => c.path === '/api/v1/recordings/export')
    const q = new URLSearchParams(fetched?.search)
    expect([q.get('path'), q.get('start'), q.get('duration')]).toEqual([
      'live/alice',
      '2026-10-03T09:00:00.000Z',
      '90.000',
    ])
    expect(calls.some((c) => c.method === 'PUT')).toBe(false)

    await user.click(within(offer).getByRole('button', { name: 'Not now' }))
    await waitFor(() => {
      expect(
        screen.queryByRole('group', { name: 'Stretch from the recording' }),
      ).not.toBeInTheDocument()
    })
  })

  it('ignores a stretch longer than a holding clip may be', async () => {
    withStream(streamer)
    renderApp('/streams/1?clipFrom=2026-10-03T09%3A00%3A00.000Z&clipSeconds=7200')
    await screen.findByRole('region', { name: 'Holding screen' })
    expect(
      screen.queryByRole('group', { name: 'Stretch from the recording' }),
    ).not.toBeInTheDocument()
  })
})
