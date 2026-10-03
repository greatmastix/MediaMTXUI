import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { createMemoryHistory, RouterProvider } from '@tanstack/react-router'
import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'

import { setCsrfToken } from '@/api/client'
import type { Session } from '@/api/sidecar'
import { createAppRouter } from '@/router'
import { installFakeEventSource } from '@/test/fakeEventSource'
import { adminSession, fakeSidecar } from '@/test/fakeSidecar'

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

describe('credentials', () => {
  it('lists active credentials first, then expired, then revoked, by name within each', async () => {
    const { sortedByState } = await import('@/api/credentials')
    const c = (name: string, state: 'active' | 'expired' | 'revoked') =>
      ({ name, state }) as Parameters<typeof sortedByState>[0][number]
    expect(
      sortedByState([c('b', 'revoked'), c('z', 'active'), c('a', 'expired'), c('c', 'active')]).map(
        (x) => x.name,
      ),
    ).toEqual(['c', 'z', 'a', 'b'])
  })

  it('offers bearer tokens for what takes them: WHIP and WHEP, not HLS', async () => {
    fakeSidecar({ session: adminSession })
    renderApp('/credentials')
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: 'New credential' }))
    const token = within(screen.getByLabelText('Kind')).getByRole('option', { name: /^Bearer/ })
    expect(token).toHaveTextContent('WHIP and WHEP')
    expect(token).not.toHaveTextContent('HLS')
  })

  it('creates a credential and shows its secret once, with ready addresses', async () => {
    const { calls } = fakeSidecar({ session: adminSession })
    renderApp('/credentials')
    const user = userEvent.setup()
    expect(await screen.findByTestId('cred-devpub')).toHaveTextContent('publish')
    await user.click(screen.getByRole('button', { name: 'New credential' }))
    await user.type(screen.getByLabelText('Name'), 'devpub')
    expect(screen.getByTestId('cred-problem')).toHaveTextContent('exists')
    await user.clear(screen.getByLabelText('Name'))
    await user.type(screen.getByLabelText('Name'), 'obs')
    await user.click(screen.getByRole('checkbox', { name: /^Read/ }))
    await user.click(screen.getByRole('checkbox', { name: /^Publish/ }))
    await user.type(screen.getByLabelText('Paths'), 'studio')
    await user.selectOptions(screen.getByLabelText('Expires'), 'In a week')
    await user.click(screen.getByRole('button', { name: 'Create' }))

    const panel = await screen.findByTestId('cred-created')
    expect(within(panel).getByTestId('cred-secret')).toHaveTextContent('SECRETSECRETSECRET2345')
    const urls = within(panel).getByRole('list', { name: 'Addresses with the credential' })
    expect(urls).toHaveTextContent('rtsp://obs:SECRETSECRETSECRET2345@mtx.example.com:8554/studio')
    expect(urls).toHaveTextContent(
      'srt://mtx.example.com:8890?streamid=publish:studio:obs:SECRETSECRETSECRET2345',
    )
    expect(
      calls.find((c) => c.method === 'POST' && c.path === '/api/v1/credentials')?.body,
    ).toEqual({
      name: 'obs',
      kind: 'password',
      actions: ['publish'],
      paths: ['studio'],
      sources: [],
      expiresInHours: 168,
    })

    await user.click(within(panel).getByRole('button', { name: 'I have copied it' }))
    expect(screen.queryByTestId('cred-secret')).not.toBeInTheDocument()
  })

  it('revokes after confirming and says what it closed', async () => {
    fakeSidecar({ session: adminSession })
    renderApp('/credentials')
    const user = userEvent.setup()
    const row = await screen.findByTestId('cred-devpub')
    await user.click(within(row).getByRole('button', { name: 'Revoke' }))
    await user.click(within(row).getByRole('button', { name: 'Revoke devpub' }))
    expect(await screen.findByTestId('cred-note')).toHaveTextContent(
      'devpub is revoked; 2 open streams were closed.',
    )
    expect(within(screen.getByTestId('cred-devpub')).getByText('revoked')).toBeInTheDocument()
  })

  it('is for admins', async () => {
    const operator: Session = { ...adminSession, user: { ...adminSession.user, role: 'operator' } }
    fakeSidecar({ session: operator })
    renderApp('/credentials')
    expect(await screen.findByText('Stream credentials are for admins.')).toBeInTheDocument()
    expect(screen.queryByRole('link', { name: 'Credentials' })).not.toBeInTheDocument()
  })
})
