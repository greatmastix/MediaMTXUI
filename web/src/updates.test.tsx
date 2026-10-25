import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { createMemoryHistory, RouterProvider } from '@tanstack/react-router'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'

import { setCsrfToken } from '@/api/client'
import type { Updates } from '@/api/updates'
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
}

const newRelease: Updates = {
  current: 'v1.4.0',
  checking: true,
  locked: '',
  latest: '1.5.0',
  page: 'https://github.com/greatmastix/MediaMTXUI/releases/tag/v1.5.0',
  available: true,
  show: true,
}

/** The fake sidecar with updates as given, which the answers and the switch change as the sidecar would. */
function withUpdates(start: Updates, session = adminSession) {
  const u = { ...start }
  const fake = fakeSidecar(
    { session },
    {
      'GET /api/v1/updates': () => json(200, u),
      'PUT /api/v1/updates/dismissal': (body) => {
        if (body.action === 'skip') u.skipped = u.latest
        else u.snoozedUntil = '2026-11-01T10:00:00Z'
        u.show = false
        return json(200, u)
      },
      'PUT /api/v1/updates/settings': (body) => {
        u.checking = body.check === true
        u.show = u.show && u.checking
        u.available = u.available && u.checking
        return json(200, u)
      },
    },
  )
  return { ...fake, u }
}

describe('update notice', () => {
  it('tells admins of a new release, and keeps quiet once they skip it, but for the dashboard', async () => {
    const { calls } = withUpdates(newRelease)
    renderApp('/')
    const notice = await screen.findByTestId('update-available')
    expect(notice).toHaveTextContent('MediaMTX UI 1.5.0 is out')
    expect(notice).toHaveTextContent('This server runs v1.4.0.')
    expect(screen.getByRole('link', { name: "What's new" })).toHaveAttribute(
      'href',
      newRelease.page,
    )
    expect(screen.getByRole('link', { name: 'How to update' })).toHaveAttribute(
      'href',
      'https://github.com/greatmastix/MediaMTXUI/blob/v1.5.0/docs/upgrading.md',
    )
    await userEvent.setup().click(screen.getByRole('button', { name: 'Skip this version' }))
    await waitFor(() => {
      expect(screen.queryByTestId('update-available')).not.toBeInTheDocument()
    })
    expect(calls.find((c) => c.path === '/api/v1/updates/dismissal')?.body).toEqual({
      action: 'skip',
    })
    // Beside the version it stays, quietly.
    expect(await screen.findByTestId('new-version')).toHaveTextContent('1.5.0 is out')
  })

  it('reminds in a week when asked to', async () => {
    const { calls } = withUpdates(newRelease)
    renderApp('/')
    await screen.findByTestId('update-available')
    await userEvent.setup().click(screen.getByRole('button', { name: 'Remind me in 7 days' }))
    await waitFor(() => {
      expect(screen.queryByTestId('update-available')).not.toBeInTheDocument()
    })
    expect(calls.find((c) => c.path === '/api/v1/updates/dismissal')?.body).toEqual({
      action: 'later',
    })
  })

  it('switches the check off and on from the dashboard', async () => {
    const { calls } = withUpdates(newRelease)
    renderApp('/')
    const box = await screen.findByRole('checkbox', { name: 'Check for new versions' })
    expect(box).toBeChecked()
    const user = userEvent.setup()
    await user.click(box)
    await waitFor(() => {
      expect(box).not.toBeChecked()
    })
    expect(screen.queryByTestId('update-available')).not.toBeInTheDocument()
    expect(screen.queryByTestId('new-version')).not.toBeInTheDocument()
    await user.click(box)
    await waitFor(() => {
      expect(box).toBeChecked()
    })
    expect(calls.filter((c) => c.path === '/api/v1/updates/settings').map((c) => c.body)).toEqual([
      { check: false },
      { check: true },
    ])
  })

  it('says why the check cannot be switched on', async () => {
    withUpdates({
      current: 'dev',
      checking: false,
      locked: 'this build (dev) is no release',
      available: false,
      show: false,
    })
    renderApp('/')
    expect(await screen.findByRole('checkbox', { name: 'Check for new versions' })).toBeDisabled()
    expect(screen.getByTestId('update-check-note')).toHaveTextContent(
      'Off: this build (dev) is no release.',
    )
  })

  it('is for admins only', async () => {
    const { calls } = withUpdates(newRelease, {
      ...adminSession,
      user: { ...adminSession.user, username: 'v', role: 'viewer' },
    })
    renderApp('/')
    await screen.findByTestId('sidecar-version')
    expect(screen.queryByTestId('update-available')).not.toBeInTheDocument()
    expect(
      screen.queryByRole('checkbox', { name: 'Check for new versions' }),
    ).not.toBeInTheDocument()
    expect(calls.some((c) => c.path === '/api/v1/updates')).toBe(false)
  })
})
