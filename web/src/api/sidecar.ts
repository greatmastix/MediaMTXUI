import { queryOptions, type QueryClient } from '@tanstack/react-query'
import { z } from 'zod'

import { ApiError, request, requestNoContent, setCsrfToken } from './client'

// The sidecar's own API (MediaMTX shapes come from the generated types in mediamtx-openapi.d.ts instead). Every
// answer is validated at this boundary.

export const healthSchema = z.object({
  status: z.literal('ok'),
  version: z.string(),
  mediamtxVersion: z.string(),
})
export type Health = z.infer<typeof healthSchema>

const setupStatusSchema = z.object({ required: z.boolean(), passkeys: z.boolean().optional() })

export const sessionSchema = z.object({
  user: z.object({
    id: z.number(),
    username: z.string(),
    role: z.enum(['admin', 'operator', 'viewer', 'streamer']),
  }),
  csrfToken: z.string(),
  authMethod: z.string(),
  expiresAt: z.string(),
  warning: z.string().optional(),
})
export type Session = z.infer<typeof sessionSchema>

export const statusSchema = z.object({
  checkedAt: z.string(),
  reachable: z.boolean(),
  version: z.string(),
  expectedVersion: z.string(),
  apiProtected: z.boolean().nullable(),
  unsafe: z.string().optional(),
  warnings: z.array(z.object({ code: z.string(), message: z.string() })),
  /** Exposure control (MTXUI_EXPOSURE_CONTROL): the Exposure page and automatic exposure. */
  exposureControl: z.boolean().optional(),
})
export type Status = z.infer<typeof statusSchema>

export interface Ingest {
  rtsp: boolean
  rtmp: boolean
  srt: boolean
}

function remember(session: Session) {
  setCsrfToken(session.csrfToken)
  return session
}

export const fetchHealth = (signal?: AbortSignal) =>
  request('GET', '/api/v1/health', healthSchema, undefined, signal)

export const fetchSetupStatus = (signal?: AbortSignal) =>
  request('GET', '/api/v1/setup', setupStatusSchema, undefined, signal)

/** The signed-in session, or null when signed out. */
export async function fetchSession(signal?: AbortSignal): Promise<Session | null> {
  try {
    return remember(await request('GET', '/api/v1/session', sessionSchema, undefined, signal))
  } catch (err) {
    if (err instanceof ApiError && err.status === 401) return null
    throw err
  }
}

export const fetchStatus = (signal?: AbortSignal) =>
  request('GET', '/api/v1/status', statusSchema, undefined, signal)

export async function completeSetup(body: {
  token: string
  username: string
  password: string
  ingest: Ingest
}) {
  return remember(await request('POST', '/api/v1/setup', sessionSchema, body))
}

/** Sets the password of an invited account with its one-time join code, and signs in. */
export async function join(body: { code: string; password: string }) {
  return remember(await request('POST', '/api/v1/join', sessionSchema, body))
}

/** A password sign-in: signed in, or (TOTP on) a ticket for the code that completes it. */
export async function login(body: { username: string; password: string }) {
  const res = await request(
    'POST',
    '/api/v1/auth/login',
    z.union([z.object({ secondFactor: z.literal('totp'), ticket: z.string() }), sessionSchema]),
    body,
  )
  if ('secondFactor' in res) return res
  return remember(res)
}

/** The second step of a password sign-in: the authenticator app's code, or a recovery code. */
export async function loginCode(body: { ticket: string; code: string }) {
  return remember(await request('POST', '/api/v1/auth/login/code', sessionSchema, body))
}

const ceremonySchema = z.object({ id: z.string(), options: z.record(z.string(), z.unknown()) })

/** Signs in with a passkey: no username, no password. */
export async function loginPasskey(
  sign: (options: Record<string, unknown>) => Promise<Record<string, unknown>>,
) {
  const begin = await request('POST', '/api/v1/auth/passkey/begin', ceremonySchema)
  const credential = await sign(begin.options)
  return remember(
    await request('POST', '/api/v1/auth/passkey/finish', sessionSchema, {
      id: begin.id,
      credential,
    }),
  )
}

export async function logout() {
  await requestNoContent('POST', '/api/v1/auth/logout')
  setCsrfToken('')
}

export const healthQuery = queryOptions({
  queryKey: ['health'],
  queryFn: ({ signal }) => fetchHealth(signal),
})

export const setupQuery = queryOptions({
  queryKey: ['setup'],
  queryFn: ({ signal }) => fetchSetupStatus(signal),
})

export const sessionQuery = queryOptions({
  queryKey: ['session'],
  queryFn: ({ signal }) => fetchSession(signal),
})

/**
 * Sets who is signed in now (null: nobody) and drops every other cached answer, all of them the last person's: a
 * query keeps its data through a failed refetch, so whoever signs in next in this tab would see them (the people, their
 * sessions, the config) until their own answers arrive, or for good where they are refused. Whether setup is done
 * stays. Call it where nothing signed in is on screen any more: on the sign-in pages, or after leaving for them.
 */
export function changeSession(queryClient: QueryClient, session: Session | null) {
  queryClient.removeQueries({ predicate: (q) => q.queryKey[0] !== setupQuery.queryKey[0] })
  queryClient.setQueryData(sessionQuery.queryKey, session)
}

export const statusQuery = queryOptions({
  queryKey: ['status'],
  queryFn: ({ signal }) => fetchStatus(signal),
  refetchInterval: 15_000,
})
