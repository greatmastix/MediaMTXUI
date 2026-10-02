import { describe, expect, expectTypeOf, it, vi } from 'vitest'

import { createMediaMTXClient, type MediaMTX } from './mediamtx'
import type { paths } from './mediamtx-openapi'

describe('generated MediaMTX types', () => {
  // Compile-time checks (tsc runs over test files): they break when a MediaMTX bump changes a shape
  // the UI relies on.
  it('describe the runtime path list', () => {
    type PathList = paths['/v3/paths/list']['get']['responses'][200]['content']['application/json']
    expectTypeOf<PathList>().toEqualTypeOf<MediaMTX['PathList']>()
    expectTypeOf<MediaMTX['Path']>().toHaveProperty('ready')
    expectTypeOf<MediaMTX['Path']>().toHaveProperty('readers')
  })

  it('describe kick endpoints', () => {
    expectTypeOf<
      paths['/v3/rtsp/sessions/kick/{id}']['post']['parameters']['path']
    >().toEqualTypeOf<{
      id: string
    }>()
  })
})

describe('createMediaMTXClient', () => {
  it('calls MediaMTX paths under the sidecar proxy prefix', async () => {
    const fetchMock = vi.fn<(req: Request) => Promise<Response>>(() =>
      Promise.resolve(
        Response.json({ pageCount: 1, itemCount: 0, items: [] } satisfies MediaMTX['PathList']),
      ),
    )
    vi.stubGlobal('fetch', fetchMock)

    const client = createMediaMTXClient('http://ui.test/api/mtx')
    const { data, error } = await client.GET('/v3/paths/list', {
      params: { query: { itemsPerPage: 10 } },
    })

    expect(error).toBeUndefined()
    expect(data?.itemCount).toBe(0)
    const req = fetchMock.mock.calls[0]?.[0]
    expect(req?.url).toBe('http://ui.test/api/mtx/v3/paths/list?itemsPerPage=10')
  })
})
