import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { createMemoryHistory, RouterProvider } from '@tanstack/react-router'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'

import { setCsrfToken } from '@/api/client'
import type { AutoRules, Exposure } from '@/api/exposure'
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

/** The fake sidecar with exposure control installed: one port open, and the rules as the server keeps them. */
function withExposure() {
  let rules: AutoRules = { publish: true, remember: true, viewers: true }
  let open = true
  const view = (): Exposure & { clientIP: string } => ({
    installed: true,
    pending: false,
    clientIP: '203.0.113.7',
    rules,
    status: {
      rev: 1,
      updated: '2026-10-03T10:00:00Z',
      driver: 'ufw',
      ports: { rtmp: { proto: 'tcp', port: 1935, state: open ? 'open' : 'closed' } },
      limits: {
        allowAnySource: true,
        maxTTLAnySource: '720h0m0s',
        maxTTL: '720h0m0s',
        permanentOK: false,
        maxSources: 8,
        minPrefixV4: 16,
        minPrefixV6: 48,
      },
    },
  })
  const fake = fakeSidecar(
    { session: adminSession },
    {
      'GET /api/v1/exposure': () => json(200, view()),
      'PUT /api/v1/exposure/auto': (body) => {
        rules = body as unknown as AutoRules
        return json(200, rules)
      },
      'POST /api/v1/exposure/close-all': () => {
        rules = { publish: false, remember: false, viewers: false }
        open = false
        return json(200, view())
      },
    },
  )
  return fake
}

describe('exposure', () => {
  it('starts the next rule change from what Close all left, not from an earlier choice', async () => {
    const { calls } = withExposure()
    renderApp('/exposure')
    const user = userEvent.setup()
    const remember = await screen.findByRole('checkbox', { name: /Remember encoders/ })
    const viewers = screen.getByRole('checkbox', { name: /Let players and viewers in/ })
    await user.click(remember)
    await waitFor(() => {
      expect(calls.filter((c) => c.path === '/api/v1/exposure/auto')).toHaveLength(1)
    })
    expect(remember).not.toBeChecked()

    await user.click(screen.getByRole('button', { name: /Close all exposure/ }))
    await user.click(screen.getByRole('button', { name: 'Close all' }))
    // Everything is off now, as the server says.
    await waitFor(() => {
      expect(viewers).not.toBeChecked()
    })
    expect(screen.getByRole('checkbox', { name: /Let encoders in/ })).not.toBeChecked()

    await user.click(remember)
    await waitFor(() => {
      expect(calls.filter((c) => c.path === '/api/v1/exposure/auto')).toHaveLength(2)
    })
    expect(calls.filter((c) => c.path === '/api/v1/exposure/auto')[1]?.body).toEqual({
      publish: false,
      remember: true,
      viewers: false,
    })
  })

  it('points to files the published repository has when the helper is missing', async () => {
    fakeSidecar(
      { session: adminSession },
      {
        'GET /api/v1/exposure': () => json(200, { installed: false, pending: false, clientIP: '' }),
      },
    )
    renderApp('/exposure')
    const panel = await screen.findByTestId('exposure-not-installed')
    expect(panel).toHaveTextContent('docs/exposure-control.md')
    expect(panel).toHaveTextContent('deploy/portgate/install.sh')
    expect(panel).not.toHaveTextContent('deploy/host')
  })
})
