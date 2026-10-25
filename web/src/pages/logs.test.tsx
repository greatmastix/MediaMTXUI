import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { createMemoryHistory, RouterProvider } from '@tanstack/react-router'
import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'

import { setCsrfToken } from '@/api/client'
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

describe('logs', () => {
  it('warns while MediaMTX logs at debug level, and only then', async () => {
    const { state } = fakeSidecar({ session: adminSession })
    state.settings.logLevel = 'debug'
    renderApp('/logs')
    expect(await screen.findByTestId('debug-log-warning')).toHaveTextContent(
      'MediaMTX logs at debug level. At debug level MediaMTX writes the stream key or guest key of every connection into its log file on the server, in clear.',
    )
  })

  it('says nothing at info level', async () => {
    fakeSidecar({ session: adminSession })
    renderApp('/logs')
    expect(await screen.findByTestId('log-state')).toBeInTheDocument()
    expect(screen.queryByTestId('debug-log-warning')).not.toBeInTheDocument()
  })
})
