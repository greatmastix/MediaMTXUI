import createClient from 'openapi-fetch'

import type { components, paths } from './mediamtx-openapi'

/** MediaMTX API schemas at the pinned version, generated from spec/mediamtx/openapi.yaml. */
export type MediaMTX = components['schemas']

/**
 * Typed client for the MediaMTX API as proxied by the sidecar.
 * Paths are MediaMTX's own, e.g. `client.GET('/v3/paths/list')`.
 */
export function createMediaMTXClient(baseUrl = '/api/mtx') {
  return createClient<paths>({ baseUrl })
}
