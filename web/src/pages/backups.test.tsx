import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { createMemoryHistory, RouterProvider } from '@tanstack/react-router'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'

import type { Backups } from '@/api/backups'
import { setCsrfToken } from '@/api/client'
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

const backups: Backups = {
  configured: true,
  schedule: { enabled: true, time: '03:30', keep: 7 },
  last: null,
  backups: [],
  maxUploadBytes: 1024 * 1024,
  staged: null,
}

describe('backups', () => {
  it('says why a file larger than the server takes is not uploaded', async () => {
    const { calls } = fakeSidecar(
      { session: adminSession },
      { 'GET /api/v1/backups': () => json(200, backups) },
    )
    renderApp('/backups')
    const input = await screen.findByLabelText(/Restore from a file/)
    const file = new File([new Uint8Array(2 * 1024 * 1024)], 'studio.mtxbackup')
    await userEvent.setup().upload(input, file)
    expect(await screen.findByRole('alert')).toHaveTextContent(
      'studio.mtxbackup is 2.00 MiB; this server takes backup files up to 1.00 MiB (MTXUI_BACKUP_MAX_UPLOAD_MB).',
    )
    expect(calls.some((c) => c.path === '/api/v1/backups/upload')).toBe(false)
  })

  it('says which large holding clips a backup leaves out', async () => {
    fakeSidecar(
      { session: adminSession },
      {
        'GET /api/v1/backups': () =>
          json(200, {
            ...backups,
            backups: [
              {
                name: 'mtxui-20261025-033000-scheduled.mtxbackup',
                kind: 'scheduled',
                size: 5_000_000,
                created: '2026-10-25T03:30:00Z',
                host: 'mtx.example.com',
                version: 'v1.4.0',
                leftOutClips: 2,
              },
            ],
          }),
      },
    )
    renderApp('/backups')
    expect(await screen.findByTestId('backup-left-out')).toHaveTextContent(
      'without 2 large holding clips',
    )
  })

  it('names the streams a restore leaves without their holding clip', async () => {
    fakeSidecar(
      { session: adminSession },
      {
        'GET /api/v1/backups': () =>
          json(200, {
            ...backups,
            staged: {
              name: 'mtxui-20261025-033000-scheduled.mtxbackup',
              created: '2026-10-25T03:30:00Z',
              version: 'v1.4.0',
              mediamtx: '1.21.3',
              database: {
                schema: 20,
                users: ['admin'],
                admins: 1,
                streams: ['live/big'],
                credentials: 1,
                snapshots: 3,
                historyMinutes: 60,
                auditEntries: 10,
              },
              currentUsers: ['admin'],
              currentStreams: ['live/big'],
              clips: 8,
              missingClips: ['live/big'],
              configChanges: false,
              expiresAt: '2099-10-25T04:00:00Z',
            },
          }),
      },
    )
    renderApp('/backups')
    expect(await screen.findByTestId('preview-missing-clips')).toHaveTextContent(
      'Holding clips too large for backups, and not on this server: live/big. These streams show the offline screen after the restore',
    )
  })
})
