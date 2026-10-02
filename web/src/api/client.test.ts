import { describe, expect, it, vi } from 'vitest'
import { z } from 'zod'

import { ApiError, request, requestNoContent, setCsrfToken } from './client'

describe('request', () => {
  it('validates answers and sends the CSRF token only on state-changing requests', async () => {
    const fetchMock = vi.fn<(input: string, init: RequestInit) => Promise<Response>>(() =>
      Promise.resolve(Response.json({ ok: true })),
    )
    vi.stubGlobal('fetch', fetchMock)
    setCsrfToken('tok')

    await expect(request('GET', '/api/x', z.object({ ok: z.boolean() }))).resolves.toEqual({
      ok: true,
    })
    await requestNoContent('POST', '/api/y', { a: 1 })

    const [, get] = fetchMock.mock.calls[0] ?? []
    const [, post] = fetchMock.mock.calls[1] ?? []
    expect(new Headers(get?.headers).get('X-CSRF-Token')).toBeNull()
    expect(new Headers(post?.headers).get('X-CSRF-Token')).toBe('tok')
    expect(new Headers(post?.headers).get('Content-Type')).toBe('application/json')
    expect(post?.body).toBe('{"a":1}')
  })

  it("turns the sidecar's error shape into an ApiError", async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(() =>
        Promise.resolve(
          Response.json({ error: 'locked', message: 'Try again later.' }, { status: 429 }),
        ),
      ),
    )
    const err: unknown = await request('POST', '/api/v1/auth/login', z.object({})).catch(
      (e: unknown) => e,
    )
    expect(err).toBeInstanceOf(ApiError)
    expect(err).toMatchObject({ status: 429, kind: 'locked', message: 'Try again later.' })
  })

  it('keeps a generic message when the error is not JSON', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(() => Promise.resolve(new Response('bad gateway', { status: 502 }))),
    )
    await expect(request('GET', '/api/x', z.object({}))).rejects.toMatchObject({
      status: 502,
      kind: 'http',
      message: 'The server answered 502.',
    })
  })

  it('rejects answers that do not match the schema', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(() => Promise.resolve(Response.json({ ok: 'yes' }))),
    )
    await expect(request('GET', '/api/x', z.object({ ok: z.boolean() }))).rejects.toThrow()
  })
})
