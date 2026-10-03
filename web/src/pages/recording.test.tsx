import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { createMemoryHistory, RouterProvider } from '@tanstack/react-router'
import { fireEvent, render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'

import { setCsrfToken } from '@/api/client'
import { createAppRouter } from '@/router'
import { installFakeEventSource } from '@/test/fakeEventSource'
import { adminSession, error, fakeSidecar, json } from '@/test/fakeSidecar'

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

const start = new Date(2026, 9, 3, 8, 0, 0)

/** A camera recorded for six hours without a gap; the server exports at most two hours at a time. */
function withRecording(tellsLimit: boolean) {
  return fakeSidecar(
    { session: adminSession },
    {
      'GET /api/v1/recordings': () =>
        json(200, {
          disk: {
            recordings: 6e9,
            free: 5e10,
            total: 1e11,
            budget: 1e11,
            minFree: 2e10,
            critical: 5e9,
            measuredAt: null,
            guard: null,
          },
          paths: [
            {
              name: 'cam1',
              segments: 6,
              first: start.toISOString(),
              last: new Date(start.getTime() + 5 * 3600_000).toISOString(),
              bytes: 6e9,
            },
          ],
          ...(tellsLimit ? { exportMaxSeconds: 7200 } : {}),
        }),
      'GET /api/v1/recordings/spans': () =>
        json(200, [{ start: start.toISOString(), duration: 6 * 3600 }]),
      'GET /api/v1/recordings/segments': () => json(200, []),
      'GET /api/v1/recordings/export': () =>
        error(400, 'invalid', 'The duration is 0 to 7200 seconds.'),
    },
  )
}

/** Selects six hours from the start, with the fields under the timeline. */
async function selectSixHours() {
  const selection = await screen.findByRole('region', { name: 'Selection' })
  fireEvent.change(within(selection).getByLabelText('From'), {
    target: { value: '2026-10-03T08:00:00' },
  })
  fireEvent.change(within(selection).getByLabelText('Length (seconds)'), {
    target: { value: String(6 * 3600) },
  })
  expect(within(selection).getByTestId('selection-summary')).toHaveTextContent('6h 0m')
  return selection
}

describe('recording', () => {
  it('says how much one export may cover, and offers no longer one', async () => {
    const { calls } = withRecording(true)
    renderApp('/recordings/cam1')
    const selection = await selectSixHours()
    expect(await within(selection).findByTestId('export-too-long')).toHaveTextContent(
      'Play and download take at most 2h 0m at a time',
    )
    expect(within(selection).getByRole('button', { name: 'Play' })).toBeDisabled()
    expect(within(selection).getByRole('button', { name: 'Download MP4' })).toBeDisabled()
    expect(within(selection).queryByRole('link', { name: 'Download MP4' })).not.toBeInTheDocument()
    expect(calls.some((c) => c.path === '/api/v1/recordings/export')).toBe(false)
  })

  it("shows the server's reason when the player gets nothing", async () => {
    withRecording(false)
    renderApp('/recordings/cam1')
    const selection = await selectSixHours()
    await userEvent.setup().click(within(selection).getByRole('button', { name: 'Play' }))
    fireEvent.error(within(selection).getByTestId('recording-player'))
    expect(await within(selection).findByTestId('recording-error')).toHaveTextContent(
      'The duration is 0 to 7200 seconds.',
    )
  })
})
