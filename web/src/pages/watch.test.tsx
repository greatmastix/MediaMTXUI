import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { createMemoryHistory, RouterProvider } from '@tanstack/react-router'
import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'

import { createAppRouter } from '@/router'
import { installFakeEventSource } from '@/test/fakeEventSource'
import { fakeSidecar, json } from '@/test/fakeSidecar'

function renderApp(path: string) {
  installFakeEventSource()
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const router = createAppRouter(queryClient, createMemoryHistory({ initialEntries: [path] }))
  render(
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  )
}

describe('public watch link', () => {
  it('never shows the text of the address as its heading', async () => {
    fakeSidecar() // no such public stream: 404
    renderApp('/s/Server%20moved%3A%20sign%20in%20at%20evil%20dot%20example/now')
    expect(
      await screen.findByText('There is no public stream at this address.'),
    ).toBeInTheDocument()
    expect(screen.getByRole('heading', { level: 1 })).toHaveTextContent('Watch')
    expect(document.body).not.toHaveTextContent(/Server moved|evil/)
  })

  it("shows the stream's own title", async () => {
    fakeSidecar(
      {},
      {
        'GET /api/v1/public/streams/live/alice': () =>
          json(200, { name: 'live/alice', title: 'Alice live', live: false, available: false }),
      },
    )
    renderApp('/s/live/alice')
    expect(await screen.findByRole('heading', { name: 'Alice live' })).toBeInTheDocument()
  })
})
