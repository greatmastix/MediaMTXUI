import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { createMemoryHistory, RouterProvider } from '@tanstack/react-router'
import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'

import { setCsrfToken } from '@/api/client'
import type { Person } from '@/api/people'
import type { Session } from '@/api/sidecar'
import { createAppRouter } from '@/router'
import { FakeEventSource, installFakeEventSource } from '@/test/fakeEventSource'
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
  return { router, queryClient }
}

const person = (p: Partial<Person> & Pick<Person, 'id' | 'username'>): Person => ({
  role: 'streamer',
  disabled: false,
  pending: false,
  joinExpires: null,
  streams: [],
  createdAt: '2026-09-29T10:00:00Z',
  totp: false,
  passkeys: 0,
  ...p,
})

const people = [
  person({ id: 1, username: 'admin', role: 'admin', totp: true }),
  person({ id: 2, username: 'boss', role: 'admin', totp: true }),
]

const viewer: Session = { ...adminSession, user: { id: 9, username: 'vera', role: 'viewer' } }

describe('people', () => {
  it("shows nothing of the last person's to whoever signs in next in the same tab", async () => {
    const { state, calls } = fakeSidecar(
      { session: adminSession, account: viewer },
      // The sidecar refuses the list to anyone but an admin.
      {
        'GET /api/v1/users': () =>
          state.session?.user.role === 'admin'
            ? json(200, people)
            : json(403, { error: 'forbidden', message: 'For admins only.' }),
      },
    )
    const { router, queryClient } = renderApp('/people')
    expect(await screen.findByTestId('person-boss')).toBeInTheDocument()

    // The admin's session ends while the page is open; a viewer signs in in the same tab.
    state.session = null
    await waitFor(() => {
      expect(FakeEventSource.instances.length).toBeGreaterThan(0)
    })
    act(() => {
      FakeEventSource.latest.emit('session', { state: 'ended' })
    })
    await waitFor(() => {
      expect(router.state.location.pathname).toBe('/login')
    })
    expect(queryClient.getQueryData(['people'])).toBeUndefined()
    const user = userEvent.setup()
    await user.type(await screen.findByLabelText('Username'), 'vera')
    await user.type(screen.getByLabelText('Password'), 'correct horse battery')
    const listed = calls.filter((c) => c.path === '/api/v1/users').length
    await user.click(screen.getByRole('button', { name: 'Sign in' }))

    expect(await screen.findByText('People are managed by admins.')).toBeInTheDocument()
    expect(router.state.location.pathname).toBe('/people')
    expect(screen.queryByTestId('person-boss')).not.toBeInTheDocument()
    expect(queryClient.getQueryData(['people'])).toBeUndefined()
    expect(calls.filter((c) => c.path === '/api/v1/users')).toHaveLength(listed)
  })

  it('forgets what was loaded when someone signs out', async () => {
    fakeSidecar({ session: adminSession }, { 'GET /api/v1/users': () => json(200, people) })
    const { router, queryClient } = renderApp('/people')
    expect(await screen.findByTestId('person-boss')).toBeInTheDocument()
    await userEvent.setup().click(screen.getByRole('button', { name: 'Sign out' }))
    await waitFor(() => {
      expect(router.state.location.pathname).toBe('/login')
    })
    await waitFor(() => {
      expect(queryClient.getQueryData(['people'])).toBeUndefined()
    })
    expect(queryClient.getQueryData(['session'])).toBeNull()
  })

  it('asks before making a reset code for someone who has joined', async () => {
    const { calls } = fakeSidecar(
      { session: adminSession },
      {
        'GET /api/v1/users': () => json(200, people),
        'POST /api/v1/users/2/join-code': () =>
          json(200, {
            user: people[1],
            joinCode: 'ABCD-EFGH-JKLM',
            expires: '2026-10-06T10:00:00Z',
          }),
      },
    )
    renderApp('/people')
    const row = await screen.findByTestId('person-boss')
    const user = userEvent.setup()
    await user.click(within(row).getByRole('button', { name: 'Reset password' }))
    expect(calls.some((c) => c.method === 'POST')).toBe(false)
    expect(screen.getByRole('note')).toHaveTextContent(
      'sign in as boss straight away, without their authenticator app or passkey',
    )

    await user.click(within(row).getByRole('button', { name: 'Cancel' }))
    expect(screen.queryByRole('note')).not.toBeInTheDocument()
    expect(calls.some((c) => c.method === 'POST')).toBe(false)

    await user.click(within(row).getByRole('button', { name: 'Reset password' }))
    await user.click(within(row).getByRole('button', { name: 'Make a reset code for boss' }))
    expect(await screen.findByTestId('join-code')).toHaveTextContent('ABCD-EFGH-JKLM')
    expect(screen.getByTestId('reset-code-note')).toHaveTextContent(
      'make another one: that ends this one',
    )
    expect(calls.filter((c) => c.method === 'POST')).toHaveLength(1)
  })
})
