import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { createMemoryHistory, RouterProvider } from '@tanstack/react-router'
import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'

import { setCsrfToken } from '@/api/client'
import type { Session } from '@/api/sidecar'
import { setTheme, storedTheme } from '@/lib/theme'
import { createAppRouter, safeRedirect } from '@/router'
import { FakeEventSource, installFakeEventSource, snapshot } from '@/test/fakeEventSource'
import { adminSession, fakeSidecar, healthyStatus } from '@/test/fakeSidecar'

function renderApp(path: string) {
  setCsrfToken('')
  installFakeEventSource()
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const router = createAppRouter(queryClient, createMemoryHistory({ initialEntries: [path] }))
  render(
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  )
  return router
}

/** Waits for the signed-in shell to open its event stream, then delivers the given events. */
async function stream(...events: [string, unknown][]) {
  await waitFor(() => {
    expect(FakeEventSource.instances.length).toBeGreaterThan(0)
  })
  act(() => {
    const es = FakeEventSource.latest
    if (es.readyState !== FakeEventSource.OPEN) es.open()
    for (const [type, data] of events) es.emit(type, data)
  })
}

const testPath = {
  name: 'test',
  confName: 'all_others',
  online: true,
  onlineTime: '2026-09-29T10:00:00Z',
  source: { type: 'rtspSession', id: '11111111-2222-3333-4444-555555555555' },
  tracks2: [
    { codec: 'H264', codecProps: { width: 1280, height: 720 } },
    { codec: 'Opus', codecProps: { channelCount: 2 } },
  ],
  readers: [{ type: 'hlsSession', id: '99999999-2222-3333-4444-555555555555' }],
  inboundBytes: 1_000_000,
  outboundBytes: 3_000_000,
}

const liveSnapshot = snapshot(
  {
    info: { value: { version: 'v1.21.1', started: '2026-09-29T09:00:00Z' } },
    paths: { items: [testPath, { name: 'idle', confName: 'idle', online: false, readers: [] }] },
    rtspSessions: {
      items: [
        {
          id: testPath.source.id,
          remoteAddr: '172.29.44.9:40000',
          path: 'test',
          state: 'publish',
          transport: 'TCP',
          user: 'devpub',
          created: '2026-09-29T10:00:00Z',
          inboundBytes: 1_000_000,
          outboundBytes: 0,
        },
      ],
    },
    rtspConns: { items: [] },
    rtmpConns: { available: false },
  },
  { reachable: true },
  { test: { inBps: 1_500_000, outBps: 3_000_000 } },
)

describe('setup', () => {
  it('sends a fresh install to setup, whatever page was asked for', async () => {
    fakeSidecar({ setupRequired: true })
    const router = renderApp('/login')
    expect(await screen.findByRole('heading', { name: 'Set up MediaMTX UI' })).toBeInTheDocument()
    expect(router.state.location.pathname).toBe('/setup')
  })

  it('validates before sending anything', async () => {
    const { calls } = fakeSidecar({ setupRequired: true })
    renderApp('/setup')
    const user = userEvent.setup()
    await user.type(await screen.findByLabelText('Setup token'), 'TOKEN')
    await user.type(screen.getByLabelText('Password'), 'correct horse battery')
    await user.type(screen.getByLabelText('Password again'), 'something else entirely')
    await user.click(screen.getByRole('button', { name: /finish setup/ }))
    expect(await screen.findByText('The passwords differ.')).toBeInTheDocument()
    expect(calls.some((c) => c.method === 'POST')).toBe(false)
  })

  it('creates the admin with the chosen protocols and signs in', async () => {
    const { calls } = fakeSidecar({ setupRequired: true })
    const router = renderApp('/setup')
    const user = userEvent.setup()
    await user.type(await screen.findByLabelText('Setup token'), 'TOKEN')
    await user.type(screen.getByLabelText('Password'), 'correct horse battery')
    await user.type(screen.getByLabelText('Password again'), 'correct horse battery')
    await user.click(screen.getByRole('checkbox', { name: 'RTSP' }))
    await user.click(screen.getByRole('button', { name: /finish setup/ }))

    expect(await screen.findByTestId('signed-in-as')).toHaveTextContent(
      'Signed in as admin (admin)',
    )
    expect(router.state.location.pathname).toBe('/')
    const post = calls.find((c) => c.method === 'POST' && c.path === '/api/v1/setup')
    expect(post?.body).toEqual({
      token: 'TOKEN',
      username: 'admin',
      password: 'correct horse battery',
      ingest: { rtsp: true, rtmp: false, srt: false },
    })
  })

  it("shows the server's message for a wrong token", async () => {
    fakeSidecar({ setupRequired: true })
    renderApp('/setup')
    const user = userEvent.setup()
    await user.type(await screen.findByLabelText('Setup token'), 'WRONG')
    await user.type(screen.getByLabelText('Password'), 'correct horse battery')
    await user.type(screen.getByLabelText('Password again'), 'correct horse battery')
    await user.click(screen.getByRole('button', { name: /finish setup/ }))
    expect(await screen.findByRole('alert')).toHaveTextContent('The setup token is wrong.')
  })

  it('sends visitors away from setup once it is done', async () => {
    fakeSidecar({ setupRequired: false })
    const router = renderApp('/setup')
    expect(
      await screen.findByRole('heading', { name: 'Sign in to MediaMTX UI' }),
    ).toBeInTheDocument()
    expect(router.state.location.pathname).toBe('/login')
  })
})

describe('sign-in', () => {
  it("shows the server's error, then signs in", async () => {
    fakeSidecar()
    const router = renderApp('/')
    const user = userEvent.setup()
    await user.type(await screen.findByLabelText('Username'), 'admin')
    await user.type(screen.getByLabelText('Password'), 'wrong password!')
    await user.click(screen.getByRole('button', { name: 'Sign in' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Wrong username or password.')

    await user.clear(screen.getByLabelText('Password'))
    await user.type(screen.getByLabelText('Password'), 'correct horse battery')
    await user.click(screen.getByRole('button', { name: 'Sign in' }))
    expect(await screen.findByTestId('signed-in-as')).toBeInTheDocument()
    expect(router.state.location.pathname).toBe('/')
  })

  it('returns to the page that was asked for, and only within the site', async () => {
    fakeSidecar()
    const router = renderApp('/paths')
    await waitFor(() => {
      expect(router.state.location.pathname).toBe('/login')
    })
    expect(router.state.location.search).toEqual({ redirect: '/paths' })
    const user = userEvent.setup()
    await user.type(await screen.findByLabelText('Username'), 'admin')
    await user.type(screen.getByLabelText('Password'), 'correct horse battery')
    await user.click(screen.getByRole('button', { name: 'Sign in' }))
    expect(await screen.findByRole('heading', { name: 'Paths' })).toBeInTheDocument()
    expect(router.state.location.pathname).toBe('/paths')

    expect(safeRedirect('https://evil.example/')).toBe('/')
    expect(safeRedirect('//evil.example/')).toBe('/')
    expect(safeRedirect('/\\evil.example/')).toBe('/')
    expect(safeRedirect('/paths/cam1')).toBe('/paths/cam1')
  })
})

describe('shell', () => {
  it('shows the status, warnings and version, and signs out with the CSRF token', async () => {
    const { calls } = fakeSidecar({
      session: adminSession,
      status: {
        ...healthyStatus,
        warnings: [{ code: 'proxy_scheme', message: 'Requests arrive over plain http.' }],
      },
    })
    const router = renderApp('/')
    expect(await screen.findByTestId('warning-proxy_scheme')).toHaveTextContent('plain http')
    expect(screen.getByTestId('live-indicator')).toHaveAttribute('data-state', 'connecting')
    await stream(['snapshot', liveSnapshot])
    expect(screen.getByTestId('live-indicator')).toHaveAttribute('data-state', 'live')
    expect(await screen.findByTestId('mediamtx-status')).toHaveTextContent('Running')
    expect(screen.getByText(/^Version 1\.21\.1, up for /)).toBeInTheDocument()
    expect(await screen.findByTestId('api-protected')).toHaveTextContent('Yes')

    await userEvent.setup().click(screen.getByRole('button', { name: 'Sign out' }))
    await waitFor(() => {
      expect(router.state.location.pathname).toBe('/login')
    })
    const logout = calls.find((c) => c.path === '/api/v1/auth/logout')
    expect(logout?.headers.get('X-CSRF-Token')).toBe(adminSession.csrfToken)
    expect(FakeEventSource.latest.readyState).toBe(FakeEventSource.CLOSED)
  })

  it('shows MediaMTX outages and recovers without a reload', async () => {
    fakeSidecar({ session: adminSession })
    renderApp('/')
    await stream(['snapshot', liveSnapshot])
    expect(await screen.findByTestId('online-path-test')).toBeInTheDocument()
    await stream([
      'status',
      {
        reachable: false,
        since: new Date().toISOString(),
        error: "MediaMTX's API does not answer",
      },
    ])
    expect(screen.getByTestId('live-indicator')).toHaveAttribute('data-state', 'degraded')
    expect(screen.getByTestId('mediamtx-unreachable')).toHaveTextContent(
      "MediaMTX's API does not answer",
    )
    expect(screen.getByTestId('mediamtx-status')).toHaveTextContent('Unreachable')

    await stream(
      ['status', { reachable: true, since: new Date().toISOString() }],
      ['update', { kind: 'paths', available: true, remove: ['test'] }],
    )
    expect(screen.getByTestId('live-indicator')).toHaveAttribute('data-state', 'live')
    expect(screen.queryByTestId('mediamtx-unreachable')).not.toBeInTheDocument()
    expect(screen.queryByTestId('online-path-test')).not.toBeInTheDocument()
    expect(screen.getByText(/Nothing is being published/)).toBeInTheDocument()
  })

  it('goes to sign-in when the stream says the session ended, and comes back afterwards', async () => {
    fakeSidecar({ session: adminSession })
    const router = renderApp('/paths')
    await stream(['snapshot', liveSnapshot], ['session', { state: 'ended' }])
    await waitFor(() => {
      expect(router.state.location.pathname).toBe('/login')
    })
    expect(router.state.location.search).toEqual({ redirect: '/paths' })
  })

  it('checks the session when the stream is refused', async () => {
    const { state } = fakeSidecar({ session: adminSession })
    const router = renderApp('/')
    await stream(['snapshot', liveSnapshot])
    state.session = null // expired on the server
    act(() => {
      FakeEventSource.latest.fail(true)
    })
    await waitFor(() => {
      expect(router.state.location.pathname).toBe('/login')
    })
  })

  it('shows a page for unknown addresses', async () => {
    fakeSidecar({ session: adminSession })
    renderApp('/no/such/page')
    expect(await screen.findByRole('heading', { name: 'Page not found' })).toBeInTheDocument()
  })

  it('switches the theme, dark by default', async () => {
    localStorage.clear()
    expect(storedTheme()).toBe('dark')
    fakeSidecar({ session: adminSession })
    renderApp('/')
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: 'Theme' }))
    await user.click(await screen.findByRole('menuitemradio', { name: 'Light' }))
    expect(document.documentElement).not.toHaveClass('dark')
    expect(storedTheme()).toBe('light')
    setTheme('dark')
    expect(document.documentElement).toHaveClass('dark')
    setTheme('system') // jsdom's system prefers light
    expect(document.documentElement).not.toHaveClass('dark')
    expect(storedTheme()).toBe('system')
  })
})

describe('dashboard', () => {
  it('shows counts, traffic and the online paths', async () => {
    fakeSidecar({ session: adminSession })
    renderApp('/')
    await stream(['snapshot', liveSnapshot])
    expect(await screen.findByTestId('stat-paths')).toHaveTextContent('1 online')
    expect(screen.getByText('2 in total')).toBeInTheDocument()
    const online = screen.getByRole('list', { name: 'Online paths' })
    expect(within(online).getByRole('link', { name: 'test' })).toBeInTheDocument()
    expect(within(online).getByText('H264, Opus')).toBeInTheDocument()
    expect(within(online).getByText('1.50 Mbit/s')).toBeInTheDocument()
    // The history arrives with the snapshot.
    expect(await screen.findByTestId('stat-traffic')).toHaveTextContent('6.00 kbit/s out')
    expect(
      screen.getByRole('img', {
        name: /^Bandwidth, last 5 minutes: In 2.00 kbit\/s, Out 6.00 kbit\/s$/,
      }),
    ).toBeInTheDocument()

    await stream([
      'sample',
      { t: Date.now(), inBps: 8000, outBps: 16000, paths: 2, online: 2, readers: 5, clients: 7 },
    ])
    expect(screen.getByTestId('stat-traffic')).toHaveTextContent('16.0 kbit/s out')
    expect(screen.getByTestId('stat-readers')).toHaveTextContent('5 readers')
  })
})

describe('paths', () => {
  it('lists and filters the paths', async () => {
    fakeSidecar({ session: adminSession })
    renderApp('/paths')
    await stream(['snapshot', liveSnapshot])
    const table = await screen.findByRole('table', { name: 'Paths' })
    expect(within(table).getAllByRole('row')).toHaveLength(3)
    expect(within(screen.getByTestId('path-row-test')).getByText('Online')).toBeInTheDocument()
    expect(within(screen.getByTestId('path-row-idle')).getByText('Offline')).toBeInTheDocument()
    expect(
      within(screen.getByTestId('path-row-test')).getByText('RTSP session'),
    ).toBeInTheDocument()

    await userEvent
      .setup()
      .type(screen.getByRole('searchbox', { name: 'Filter paths by name' }), 'TE')
    expect(within(table).getAllByRole('row')).toHaveLength(2)
    expect(screen.queryByTestId('path-row-idle')).not.toBeInTheDocument()
  })

  it('shows a path with its tracks and readers, and follows it live', async () => {
    fakeSidecar({ session: adminSession })
    renderApp('/paths/test')
    await stream(['snapshot', liveSnapshot])
    expect(await screen.findByRole('heading', { name: 'test' })).toBeInTheDocument()
    expect(screen.getByTestId('path-state')).toHaveTextContent('Online')
    expect(screen.getByText('width 1280 · height 720')).toBeInTheDocument()
    const readers = screen.getByRole('table', { name: 'Readers' })
    expect(within(readers).getByText('HLS session')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'RTSP session' })).toHaveAttribute(
      'href',
      `/connections/rtsp?kind=rtspSessions&id=${testPath.source.id}`,
    )

    await stream(['update', { kind: 'paths', available: true, remove: ['test'] }])
    expect(await screen.findByTestId('path-missing')).toBeInTheDocument()
  })

  it('handles names with slashes', async () => {
    fakeSidecar({ session: adminSession })
    renderApp('/paths/live/cam%201')
    await stream([
      'snapshot',
      snapshot({ paths: { items: [{ name: 'live/cam 1', online: false }] } }),
    ])
    expect(await screen.findByRole('heading', { name: 'live/cam 1' })).toBeInTheDocument()
  })
})

describe('connections', () => {
  it('lists sessions per protocol and shows one in detail', async () => {
    fakeSidecar({ session: adminSession })
    const router = renderApp('/connections')
    await stream(['snapshot', liveSnapshot])
    await waitFor(() => {
      expect(router.state.location.pathname).toBe('/connections/rtsp')
    })
    const sessions = await screen.findByRole('table', { name: 'RTSP sessions' })
    expect(within(sessions).getByText('172.29.44.9:40000')).toBeInTheDocument()
    expect(within(sessions).getByText('publish')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'RTMP off' })).toHaveTextContent('off')

    const user = userEvent.setup()
    await user.click(
      within(sessions).getByRole('link', { name: `Details of session ${testPath.source.id}` }),
    )
    const detail = await screen.findByTestId('connection-detail')
    expect(within(detail).getByText('devpub')).toBeInTheDocument()
    expect(within(detail).getByText('TCP')).toBeInTheDocument()
    await user.keyboard('{Escape}')
    await waitFor(() => {
      expect(router.state.location.search).toEqual({})
    })

    await user.click(screen.getByRole('link', { name: 'RTMP off' }))
    expect(await screen.findByTestId('off-rtmpConns')).toHaveTextContent('switched off')
  })

  it('is for operators only', async () => {
    const viewer: Session = {
      ...adminSession,
      user: { ...adminSession.user, username: 'v', role: 'viewer' },
    }
    fakeSidecar({ session: viewer })
    renderApp('/connections/rtsp')
    expect(await screen.findByText(/visible to operators and admins/)).toBeInTheDocument()
    expect(screen.queryByRole('link', { name: 'Connections' })).not.toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Paths' })).toBeInTheDocument()
  })
})
