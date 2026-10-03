import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { createMemoryHistory, RouterProvider } from '@tanstack/react-router'
import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'

import { setCsrfToken } from '@/api/client'
import { deletePath, savePath } from '@/api/config'
import { createAppRouter } from '@/router'
import { installFakeEventSource } from '@/test/fakeEventSource'
import { adminSession, fakeSidecar, type FakeState } from '@/test/fakeSidecar'

// Edits and changes made elsewhere meanwhile (another admin, a stream's owner, the sidecar itself): the forms neither
// lose what was typed when the file is fetched again, nor write back what changed meanwhile.

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

/** Someone else writes mediamtx.yml: the file and its version change on the server only. */
function changeElsewhere(state: FakeState, change: (settings: Record<string, unknown>) => void) {
  change(state.settings)
  const top = state.snapshots[0]
  if (!top) throw new Error('no snapshot')
  const id = top.id + 1
  state.snapshots.unshift({ ...top, id, sha256: `sha-${String(id)}`, parentId: top.id })
}

const pathsOf = (state: FakeState) => state.settings.paths as Record<string, unknown>

describe('editing while the file changes elsewhere', () => {
  it('keeps YAML edits when the file is fetched again, and says it changed meanwhile', async () => {
    const { calls, state } = fakeSidecar({ session: adminSession })
    const { queryClient } = renderApp('/config/yaml')
    const user = userEvent.setup()
    const editor = await screen.findByRole('textbox', { name: 'mediamtx.yml' })
    const edited = (editor as HTMLTextAreaElement).value.replace('"info"', '"warn"')
    await user.clear(editor)
    await user.click(editor)
    await user.paste(edited)

    changeElsewhere(state, (s) => {
      s.api = false
    })
    await act(() => queryClient.invalidateQueries({ queryKey: ['config'] })) // a window focus, say
    expect(await screen.findByTestId('yaml-changed-meanwhile')).toBeInTheDocument()
    expect(editor).toHaveValue(edited)

    // Saving goes against the version the edits were made from, so the server refuses it.
    await user.click(screen.getByRole('button', { name: 'Save' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('changed since you loaded it')
    expect(calls.find((c) => c.method === 'PUT')?.body).toMatchObject({ sha256: 'sha-1' })
    expect(state.settings.logLevel).toBe('info')

    await user.click(screen.getByRole('button', { name: /Load the current file/ }))
    await waitFor(() => {
      expect(editor).toHaveValue(JSON.stringify(state.settings, null, 2) + '\n')
    })
    expect(screen.queryByTestId('yaml-changed-meanwhile')).not.toBeInTheDocument()

    // Unedited, the editor follows the file.
    changeElsewhere(state, (s) => {
      s.logLevel = 'debug'
    })
    await act(() => queryClient.invalidateQueries({ queryKey: ['config'] }))
    await waitFor(() => {
      expect((editor as HTMLTextAreaElement).value).toContain('"debug"')
    })
  })

  it('keeps settings typed in, shows the others as they are now, and saves only what was typed', async () => {
    const { calls, state } = fakeSidecar({ session: adminSession })
    const { queryClient } = renderApp('/config/global')
    const user = userEvent.setup()
    const logLevel = await screen.findByLabelText('logLevel')
    await user.clear(logLevel)
    await user.type(logLevel, 'debug')

    changeElsewhere(state, (s) => {
      s.readTimeout = '30s'
    })
    await act(() => queryClient.invalidateQueries({ queryKey: ['config'] }))
    await waitFor(() => {
      expect(screen.getByLabelText('readTimeout')).toHaveValue('30s')
    })
    expect(logLevel).toHaveValue('debug')
    expect(screen.getByTestId('unsaved')).toHaveTextContent('1 unsaved change')

    await user.click(screen.getByRole('button', { name: 'Save' }))
    await screen.findByTestId('saved-note')
    expect(calls.find((c) => c.method === 'PATCH')?.body).toEqual({
      set: { logLevel: 'debug' },
      remove: [],
    })
    expect(state.settings).toMatchObject({ logLevel: 'debug', readTimeout: '30s' })
  })

  it('writes a path from the file as it is now, not from the copy the page loaded', async () => {
    const { calls, state } = fakeSidecar({ session: adminSession })
    pathsOf(state).cam1 = {
      source: 'rtsp://user:secret@10.0.0.5/live',
      forward: [{ dest: 'rtmp://live.example/app#alices-key' }],
    }
    renderApp('/config/paths/cam1')
    const user = userEvent.setup()
    await screen.findByRole('textbox', { name: 'Source URL' })

    // The stream's owner deletes the forward, and the recordings guard stops recording; this page does not refetch.
    changeElsewhere(state, (s) => {
      ;(s.paths as Record<string, unknown>).cam1 = {
        source: 'rtsp://user:secret@10.0.0.5/live',
        record: false,
      }
    })
    await user.type(screen.getByLabelText('maxReaders'), '4')
    await user.click(screen.getByRole('button', { name: 'Save' }))
    await screen.findByTestId('saved-note')
    expect(calls.find((c) => c.method === 'PUT')?.body).toEqual({
      config: { source: 'rtsp://user:secret@10.0.0.5/live', record: false, maxReaders: 4 },
    })
  })

  it('records a path as it is now', async () => {
    const { calls, state } = fakeSidecar({ session: adminSession })
    renderApp('/config')
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: /Record a stream/ }))
    await user.selectOptions(screen.getByLabelText('Record'), 'cam1')
    changeElsewhere(state, (s) => {
      ;(s.paths as Record<string, unknown>).cam1 = { source: 'rtsp://10.0.0.6/live' }
    })
    await user.click(screen.getByRole('button', { name: 'Save' }))
    await screen.findByTestId('quick-done')
    expect(calls.find((c) => c.method === 'PUT')?.body).toEqual({
      config: {
        source: 'rtsp://10.0.0.6/live',
        record: true,
        recordSegmentDuration: '1h',
        recordDeleteAfter: '168h',
      },
      reason: 'quick setup: record cam1',
    })
  })
})

describe('path names', () => {
  it('treats names such as valueOf or toString like any other', async () => {
    const { calls } = fakeSidecar({ session: adminSession })
    const { router } = renderApp('/config/new-path')
    const user = userEvent.setup()
    await user.type(await screen.findByLabelText('name'), 'valueOf')
    await user.click(screen.getByRole('button', { name: 'Create path' }))
    await waitFor(() => {
      expect(router.state.location.pathname).toBe('/config/paths/valueOf')
    })
    expect(calls.find((c) => c.method === 'PUT')?.path).toBe('/api/v1/config/paths/valueOf')

    await act(() => router.navigate({ to: '/config/paths/$', params: { _splat: 'toString' } }))
    expect(await screen.findByTestId('config-path-missing')).toHaveTextContent(
      'There is no path toString',
    )
  })

  it('refuses a name with a "." or ".." part, which a request would send to another path', async () => {
    const { calls } = fakeSidecar({ session: adminSession })
    await expect(deletePath('~^live/./(.+)$')).rejects.toThrow('"." or ".."')
    await expect(savePath('~news/../(.*)', {})).rejects.toThrow('"." or ".."')
    expect(calls).toEqual([])
    await expect(savePath('~^cams/(.+)$', {})).resolves.toMatchObject({ changed: true })
  })

  it('lists sources without their passwords, stream keys or query secrets', async () => {
    const { state } = fakeSidecar({ session: adminSession })
    Object.assign(pathsOf(state), {
      twitch: { source: 'rtmps://ingest.example/app#live_123_SECRET' },
      srtcam: { source: 'srt://10.0.0.8:8890?streamid=read:cam:user:pass&passphrase=longsecret' },
    })
    renderApp('/config/paths')
    const twitch = await screen.findByTestId('config-path-twitch')
    expect(within(twitch).getByText('rtmps://ingest.example/app#•••')).toBeInTheDocument()
    const srt = screen.getByTestId('config-path-srtcam')
    expect(srt).toHaveTextContent('srt://10.0.0.8:8890?streamid=•••&passphrase=•••')
    expect(screen.getByRole('table', { name: 'Configured paths' })).not.toHaveTextContent(
      /SECRET|longsecret|user:pass/,
    )
  })
})
