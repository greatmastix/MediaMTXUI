import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { createMemoryHistory, RouterProvider } from '@tanstack/react-router'
import { render, screen, waitFor, within } from '@testing-library/react'
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
  return router
}

describe('global settings', () => {
  it('saves only what changed, keeps locked settings read-only, and shows refusals', async () => {
    const { calls, state } = fakeSidecar({ session: adminSession })
    renderApp('/config/global')
    const user = userEvent.setup()
    const logLevel = await screen.findByLabelText('logLevel')
    expect(logLevel).toHaveValue('info')
    expect(screen.getByLabelText(/^authMethod/)).toBeDisabled()
    expect(screen.getByLabelText(/^runOnConnect(?!Restart)/)).toBeDisabled()
    // A boolean shows MediaMTX's default when unset.
    expect(
      within(screen.getByLabelText('rtsp')).getByRole('option', { name: 'Default (yes)' }),
    ).toBeInTheDocument()

    await user.clear(logLevel)
    await user.type(logLevel, 'debug')
    await user.selectOptions(screen.getByLabelText('rtsp'), 'no')
    expect(screen.getByTestId('unsaved')).toHaveTextContent('2 unsaved changes')
    await user.click(screen.getByRole('button', { name: 'Save' }))
    expect(await screen.findByTestId('saved-note')).toHaveTextContent('Saved as version 2.')
    const patch = calls.find((c) => c.method === 'PATCH')
    expect(patch?.body).toEqual({ set: { logLevel: 'debug', rtsp: false }, remove: [] })
    expect(patch?.headers.get('X-CSRF-Token')).toBe(adminSession.csrfToken)
    expect(state.settings.logLevel).toBe('debug')

    // The server's refusal is shown; a bad number never leaves the browser.
    const timeout = await screen.findByLabelText('readTimeout')
    await user.type(timeout, 'bad')
    await user.click(screen.getByRole('button', { name: 'Save' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('invalid duration')
    await user.clear(timeout)
    const queue = screen.getByLabelText('writeQueueSize')
    await user.type(queue, 'lots')
    await user.click(screen.getByRole('button', { name: 'Save' }))
    expect(await screen.findByText('Enter a whole number.')).toBeInTheDocument()
    expect(calls.filter((c) => c.method === 'PATCH')).toHaveLength(2)
  })

  it('filters settings', async () => {
    fakeSidecar({ session: adminSession })
    renderApp('/config/global')
    const user = userEvent.setup()
    await screen.findByLabelText('logLevel')
    await user.type(screen.getByRole('searchbox', { name: 'Filter settings' }), 'srtAddress')
    expect(screen.getByLabelText('srtAddress')).toBeInTheDocument()
    expect(screen.queryByLabelText('logLevel')).not.toBeInTheDocument()
  })
})

describe('paths', () => {
  it('lists configured paths without their passwords, and removes one after confirming', async () => {
    const { state } = fakeSidecar({ session: adminSession })
    renderApp('/config/paths')
    const row = await screen.findByTestId('config-path-cam1')
    expect(row).toHaveTextContent('rtsp://user:•••@10.0.0.5/live')
    expect(row).not.toHaveTextContent('secret')
    const user = userEvent.setup()
    await user.click(within(row).getByRole('button', { name: 'Remove' }))
    await user.click(within(row).getByRole('button', { name: 'Remove cam1' }))
    expect(await screen.findByTestId('saved-note')).toHaveTextContent('Path cam1 removed.')
    expect(state.settings.paths).not.toHaveProperty('cam1')
  })

  it('creates a path with a pulled source and other settings', async () => {
    const { calls } = fakeSidecar({ session: adminSession })
    const router = renderApp('/config/new-path')
    const user = userEvent.setup()
    await user.type(await screen.findByLabelText('name'), 'live/door')
    await user.selectOptions(screen.getByLabelText('source'), 'url')
    const url = screen.getByRole('textbox', { name: 'Source URL' })
    await user.clear(url)
    await user.type(url, 'rtsp://10.0.0.9/stream')
    await user.selectOptions(screen.getByLabelText('sourceOnDemand'), 'yes')
    await user.click(screen.getByRole('button', { name: 'Create path' }))
    await waitFor(() => {
      expect(router.state.location.pathname).toBe('/config/paths/live/door')
    })
    const put = calls.find((c) => c.method === 'PUT')
    expect(put?.path).toBe('/api/v1/config/paths/live/door')
    expect(put?.body).toEqual({
      config: { sourceOnDemand: true, source: 'rtsp://10.0.0.9/stream' },
    })
  })

  it('edits an existing path, keeping the settings it had', async () => {
    const { calls } = fakeSidecar({ session: adminSession })
    renderApp('/config/paths/cam1')
    const user = userEvent.setup()
    expect(await screen.findByRole('textbox', { name: 'Source URL' })).toHaveValue(
      'rtsp://user:secret@10.0.0.5/live',
    )
    await user.type(screen.getByLabelText('maxReaders'), '4')
    await user.click(screen.getByRole('button', { name: 'Save' }))
    await screen.findByTestId('saved-note')
    expect(calls.find((c) => c.method === 'PUT')?.body).toEqual({
      config: { source: 'rtsp://user:secret@10.0.0.5/live', maxReaders: 4 },
    })
  })
})

describe('YAML editor', () => {
  it('checks, saves the version it loaded, and reports a conflict', async () => {
    const { calls, state } = fakeSidecar({ session: adminSession })
    renderApp('/config/yaml')
    const user = userEvent.setup()
    const editor = await screen.findByRole('textbox', { name: 'mediamtx.yml' })
    await user.click(screen.getByRole('button', { name: 'Check' }))
    expect(await screen.findByTestId('yaml-valid')).toBeInTheDocument()

    const text = (editor as HTMLTextAreaElement).value.replace('"info"', '"warn"')
    await user.clear(editor)
    await user.click(editor)
    await user.paste(text)
    await user.click(screen.getByRole('button', { name: 'Save' }))
    expect(await screen.findByTestId('saved-note')).toHaveTextContent('Saved as version 2.')
    expect(calls.find((c) => c.method === 'PUT')?.body).toMatchObject({ sha256: 'sha-1' })
    expect(state.settings.logLevel).toBe('warn')

    // Someone else saves in between: the next save is refused, not merged.
    const top = state.snapshots[0]
    if (!top) throw new Error('no snapshot')
    state.snapshots.unshift({ ...top, id: 9, sha256: 'sha-9' })
    await user.type(await screen.findByRole('textbox', { name: 'mediamtx.yml' }), ' ')
    await user.click(screen.getByRole('button', { name: 'Save' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('changed since you loaded it')
  })
})

describe('history', () => {
  it('shows what a version changed and restores an old one', async () => {
    const { state } = fakeSidecar({ session: adminSession })
    state.settings.logLevel = 'debug'
    // A second version on top of the initial one.
    const initial = state.snapshots[0]
    if (!initial) throw new Error('no snapshot')
    state.snapshots.unshift({
      ...initial,
      id: 2,
      sha256: 'sha-2',
      reason: 'more logging',
      parentId: 1,
      content: JSON.stringify(state.settings, null, 2) + '\n',
    })
    renderApp('/config/history')
    const user = userEvent.setup()
    const diff = await screen.findByTestId('diff')
    expect(within(diff).getByText('"logLevel": "debug",')).toBeInTheDocument()
    expect(within(diff).getByText('"logLevel": "info",')).toBeInTheDocument()

    await user.click(screen.getByRole('link', { name: /Version 1/ }))
    await user.click(await screen.findByRole('button', { name: 'Restore…' }))
    await user.click(screen.getByRole('button', { name: 'Restore version 1' }))
    expect(await screen.findByTestId('saved-note')).toHaveTextContent('Restored as version 3.')
    expect(state.settings.logLevel).toBe('info')
  })
})

describe('access', () => {
  it('offers configuration to admins only', async () => {
    const operator: Session = { ...adminSession, user: { ...adminSession.user, role: 'operator' } }
    fakeSidecar({ session: operator })
    renderApp('/config/global')
    expect(await screen.findByText('The configuration is for admins.')).toBeInTheDocument()
    expect(screen.queryByRole('link', { name: 'Configuration' })).not.toBeInTheDocument()
  })

  it('lets an admin dismiss the notice about an outside edit', async () => {
    const { calls, state } = fakeSidecar({ session: adminSession })
    state.status = {
      ...state.status,
      warnings: [
        { code: 'config_drift', message: 'mediamtx.yml was changed outside the sidecar.' },
      ],
    }
    renderApp('/')
    const banner = await screen.findByTestId('warning-config_drift')
    await userEvent.setup().click(within(banner).getByRole('button', { name: 'Dismiss' }))
    await waitFor(() => {
      expect(calls.some((c) => c.path === '/api/v1/config/drift/dismiss')).toBe(true)
    })
  })
})

describe('quick setup', () => {
  const start = async (task: RegExp) => {
    renderApp('/config')
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: task }))
    return user
  }

  it('is where configuration starts, and re-streams a camera', async () => {
    const { calls } = fakeSidecar({ session: adminSession })
    const user = await start(/Re-stream a camera/)
    expect(screen.getByTestId('quick-problem')).toHaveTextContent('Give the path a name.')
    await user.type(screen.getByLabelText('Path name'), 'cam1')
    expect(screen.getByTestId('quick-problem')).toHaveTextContent('There is already a path cam1')
    await user.clear(screen.getByLabelText('Path name'))
    await user.type(screen.getByLabelText('Path name'), 'garage')
    await user.type(screen.getByLabelText('Pull from'), 'rtsp://10.0.0.7/live')
    expect(screen.getByRole('list', { name: 'Changes' })).toHaveTextContent(
      'Add path garage, pulled from rtsp://10.0.0.7/live while someone watches.',
    )
    await user.click(screen.getByRole('button', { name: 'Save' }))
    const done = await screen.findByTestId('quick-done')
    expect(done).toHaveTextContent('garage is set up.')
    expect(done).toHaveTextContent('rtsp://mtx.example.com:8554/garage')
    expect(done).toHaveTextContent('srt://mtx.example.com:8890?streamid=read:garage')
    expect(calls.find((c) => c.method === 'PUT')?.body).toMatchObject({
      config: { source: 'rtsp://10.0.0.7/live', sourceOnDemand: true },
    })
  })

  it('prepares a path for a publisher, switching its protocol on first', async () => {
    const { calls, state } = fakeSidecar({ session: adminSession })
    state.settings.srt = false
    const user = await start(/Receive a stream/)
    await user.type(screen.getByLabelText('Path name'), 'studio')
    await user.selectOptions(screen.getByLabelText('The publisher uses'), 'srt')
    expect(screen.getByRole('list', { name: 'Changes' })).toHaveTextContent(
      'Switch the SRT server on.',
    )
    await user.click(screen.getByRole('button', { name: 'Save' }))
    const done = await screen.findByTestId('quick-done')
    expect(done).toHaveTextContent('srt://mtx.example.com:8890?streamid=publish:studio')
    expect(done).toHaveTextContent('a stream credential with the publish action for studio')
    const writes = calls.filter((c) => c.method !== 'GET').map((c) => `${c.method} ${c.path}`)
    expect(writes).toEqual(['PATCH /api/v1/config/global', 'PUT /api/v1/config/paths/studio'])
    expect(state.settings.srt).toBe(true)
  })

  it('forwards a path, keeping its settings', async () => {
    const { calls } = fakeSidecar({ session: adminSession })
    const user = await start(/Forward a stream/)
    await user.selectOptions(screen.getByLabelText('Forward'), 'cam1')
    await user.type(screen.getByLabelText('To'), 'rtmp://a.rtmp.youtube.com/live2#secret-key')
    expect(screen.getByRole('list', { name: 'Changes' })).toHaveTextContent(
      'Forward cam1 to rtmp://a.rtmp.youtube.com/live2#… whenever it is live.',
    )
    await user.click(screen.getByRole('button', { name: 'Save' }))
    await screen.findByTestId('quick-done')
    expect(calls.find((c) => c.method === 'PUT')?.body).toEqual({
      config: {
        source: 'rtsp://user:secret@10.0.0.5/live',
        forward: [{ dest: 'rtmp://a.rtmp.youtube.com/live2#secret-key' }],
      },
      reason: 'quick setup: forward cam1',
    })
  })

  it('records every path through the path defaults', async () => {
    const { state } = fakeSidecar({ session: adminSession })
    const user = await start(/Record a stream/)
    await user.selectOptions(screen.getByLabelText('Record'), 'Every path (the path defaults)')
    await user.click(screen.getByRole('button', { name: 'Save' }))
    await screen.findByTestId('quick-done')
    expect(state.settings.pathDefaults).toEqual({
      record: true,
      recordSegmentDuration: '1h',
      recordDeleteAfter: '168h',
    })
  })

  it('switches protocols and says what that interrupts', async () => {
    const { state } = fakeSidecar({ session: adminSession })
    const user = await start(/Choose the protocols/)
    expect(screen.getByTestId('quick-problem')).toHaveTextContent('Nothing changed yet.')
    await user.click(screen.getByRole('checkbox', { name: 'RTMP' }))
    expect(screen.getByRole('list', { name: 'Changes' })).toHaveTextContent(
      'Switch the RTMP server off.',
    )
    expect(screen.getByText(/Saving restarts the RTMP server/)).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Save' }))
    await screen.findByTestId('quick-done')
    expect(state.settings.rtmp).toBe(false)
  })
})
