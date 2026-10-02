import { queryOptions } from '@tanstack/react-query'
import { z } from 'zod'

import { request } from './client'

// Stream credentials (admin only): what publishers and readers give MediaMTX. The secret comes back once, when the
// credential is created.

export const credentialSchema = z.object({
  name: z.string(),
  kind: z.enum(['password', 'token']),
  actions: z.array(z.string()),
  paths: z.array(z.string()),
  sources: z.array(z.string()),
  expiresAt: z.string().nullable(),
  revokedAt: z.string().nullable(),
  lastUsedAt: z.string().nullable(),
  createdAt: z.string(),
  createdBy: z.string(),
  state: z.enum(['active', 'expired', 'revoked']),
  secret: z.string().optional(),
})
export type Credential = z.infer<typeof credentialSchema>

export const credentialsQuery = queryOptions({
  queryKey: ['credentials'],
  queryFn: ({ signal }) =>
    request('GET', '/api/v1/credentials', z.array(credentialSchema), undefined, signal),
})

export interface NewCredential {
  name: string
  kind: 'password' | 'token'
  actions: string[]
  paths: string[]
  sources: string[]
  expiresInHours: number
}

export const createCredential = (c: NewCredential) =>
  request('POST', '/api/v1/credentials', credentialSchema, c)

export const revokeCredential = (name: string) =>
  request(
    'POST',
    `/api/v1/credentials/${encodeURIComponent(name)}/revoke`,
    z.object({ kicked: z.number() }),
  )

const stateOrder: Record<Credential['state'], number> = { active: 0, expired: 1, revoked: 2 }

/** Active credentials first, then expired, then revoked; by name within each. */
export function sortedByState(list: readonly Credential[]): Credential[] {
  return [...list].sort(
    (a, b) => stateOrder[a.state] - stateOrder[b.state] || a.name.localeCompare(b.name),
  )
}
