import { queryOptions } from '@tanstack/react-query'
import { z } from 'zod'

import { ApiError, request, requestNoContent } from './client'

// The sidecar's config API (admin only). Every write is validated by the sidecar's rules and MediaMTX's own parser,
// written atomically and recorded as a snapshot; structured edits leave the rest of mediamtx.yml byte for byte.

const snapshotSchema = z.object({
  id: z.number(),
  at: z.string(),
  sha256: z.string(),
  author: z.string(),
  reason: z.string(),
  parentId: z.number().nullable(),
  content: z.string().optional(),
})
export type Snapshot = z.infer<typeof snapshotSchema>

const settingsValue = z.record(z.string(), z.unknown())

export const configSchema = z.object({
  content: z.string(),
  sha256: z.string(),
  snapshot: snapshotSchema.nullable(),
  settings: settingsValue.nullable(),
  locked: z.record(z.string(), z.string()),
  publicHost: z.string(),
})
export type Config = z.infer<typeof configSchema>

const appliedSchema = z.object({
  state: z.enum(['verified', 'mismatch', 'skipped', 'restored']),
  message: z.string(),
  keys: z.array(z.string()).optional(),
})
export type Applied = z.infer<typeof appliedSchema>

const writeResultSchema = z.object({
  changed: z.boolean(),
  sha256: z.string(),
  snapshot: snapshotSchema.optional(),
  applied: appliedSchema.optional(),
})
export type WriteResult = z.infer<typeof writeResultSchema>

export const configQuery = queryOptions({
  queryKey: ['config'],
  queryFn: ({ signal }) => request('GET', '/api/v1/config', configSchema, undefined, signal),
})

export const snapshotsQuery = queryOptions({
  queryKey: ['config', 'snapshots'],
  queryFn: ({ signal }) =>
    request('GET', '/api/v1/config/snapshots', z.array(snapshotSchema), undefined, signal),
})

export const snapshotQuery = (id: number) =>
  queryOptions({
    queryKey: ['config', 'snapshots', id],
    queryFn: ({ signal }) =>
      request('GET', `/api/v1/config/snapshots/${id}`, snapshotSchema, undefined, signal),
    staleTime: Infinity, // a snapshot never changes
  })

export const validateConfig = (content: string) =>
  request('POST', '/api/v1/config/validate', z.object({ valid: z.boolean() }), { content })

export const replaceConfig = (content: string, sha256: string, reason?: string) =>
  request('PUT', '/api/v1/config', writeResultSchema, { content, sha256, reason })

export const restoreSnapshot = (id: number) =>
  request('POST', `/api/v1/config/snapshots/${id}/restore`, writeResultSchema)

/**
 * A path name in a URL: its slashes stay path separators, everything else is escaped. A "." or ".." between slashes
 * (possible only in a regular expression's name) stays as it is, and URL parsing removes it, so the request would name
 * another path, perhaps one that exists: such a name is refused here, and changed in the YAML editor instead.
 */
export function pathURL(name: string) {
  const parts = name.split('/')
  if (parts.some((p) => p === '.' || p === '..')) {
    throw new ApiError(
      400,
      'invalid',
      `${name} has a "." or ".." between slashes, which an address cannot carry; change this path in the YAML editor.`,
    )
  }
  return `/api/v1/config/paths/${parts.map(encodeURIComponent).join('/')}`
}

export async function savePath(name: string, config: Record<string, unknown>, reason?: string) {
  return request('PUT', pathURL(name), writeResultSchema, { config, reason })
}

export async function deletePath(name: string) {
  return request('DELETE', pathURL(name), writeResultSchema)
}

export interface SettingsPatch {
  set: Record<string, unknown>
  remove: string[]
  reason?: string
}

export const patchGlobal = (p: SettingsPatch) =>
  request('PATCH', '/api/v1/config/global', writeResultSchema, p)

export const patchPathDefaults = (p: SettingsPatch) =>
  request('PATCH', '/api/v1/config/path-defaults', writeResultSchema, p)

export const dismissDrift = () => requestNoContent('POST', '/api/v1/config/drift/dismiss')

/** The metadata of every MediaMTX setting, generated from the spec and reference config; loaded on demand. */
export interface Setting {
  key: string
  section: string
  type: 'boolean' | 'string' | 'integer' | 'number' | 'array' | 'object'
  items?: 'string' | 'integer' | 'number' | 'object' | 'boolean'
  default: unknown
  description: string
  nullable?: boolean
}

export interface SettingsCatalog {
  version: string
  global: Setting[]
  path: Setting[]
}

export const catalogQuery = queryOptions({
  queryKey: ['settings-catalog'],
  queryFn: async () =>
    (await import('./mediamtx-settings.json')).default as unknown as SettingsCatalog,
  staleTime: Infinity,
})

/** The note after a write: what was saved, and whether MediaMTX runs with it. */
export function describeWrite(
  res: WriteResult,
  what = 'Saved',
): { message: string; warn: boolean } {
  if (!res.changed) return { message: 'Nothing changed.', warn: false }
  const saved = `${what} as version ${String(res.snapshot?.id)}.`
  switch (res.applied?.state) {
    case 'verified':
      return { message: `${saved} MediaMTX applied it.`, warn: false }
    case 'mismatch':
    case 'skipped':
      return { message: `${saved} ${res.applied.message}`, warn: true }
    default:
      return { message: saved, warn: false }
  }
}
