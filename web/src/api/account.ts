import { queryOptions } from '@tanstack/react-query'
import { z } from 'zod'

import { request, requestNoContent } from './client'

// The signed-in user's own account: password, sessions, authenticator app and recovery codes,
// passkeys, and whether admin-level changes ask them to confirm it is them.

export const passkeySchema = z.object({
  id: z.number(),
  name: z.string(),
  createdAt: z.string(),
  lastUsedAt: z.string().nullable(),
})
export type Passkey = z.infer<typeof passkeySchema>

export const accountSchema = z.object({
  username: z.string(),
  role: z.string(),
  totp: z.boolean(),
  recoveryCodesLeft: z.number(),
  passkeys: z.array(passkeySchema),
  passkeysAvailable: z.boolean(),
  stepUpOptOut: z.boolean(),
  verifiedAt: z.string().nullable(),
  stepUpDue: z.boolean(),
})
export type Account = z.infer<typeof accountSchema>

export const accountQuery = queryOptions({
  queryKey: ['account'],
  queryFn: ({ signal }) => request('GET', '/api/v1/account', accountSchema, undefined, signal),
})

export const changePassword = (current: string, next: string) =>
  requestNoContent('POST', '/api/v1/account/password', { current, new: next })

export const sessionViewSchema = z.object({
  handle: z.string(),
  current: z.boolean(),
  method: z.string(),
  createdAt: z.string(),
  lastSeenAt: z.string(),
  ip: z.string(),
  userAgent: z.string(),
})
export type SessionView = z.infer<typeof sessionViewSchema>

export const mySessionsQuery = queryOptions({
  queryKey: ['account', 'sessions'],
  queryFn: ({ signal }) =>
    request('GET', '/api/v1/account/sessions', z.array(sessionViewSchema), undefined, signal),
})

/** Signs one of your sessions out; "others" signs out every one but this. */
export const endMySession = (handle: string) =>
  requestNoContent('DELETE', `/api/v1/account/sessions/${encodeURIComponent(handle)}`)

export const totpSetup = () =>
  request('POST', '/api/v1/account/totp/setup', z.object({ secret: z.string(), uri: z.string() }))

const codesSchema = z.object({ recoveryCodes: z.array(z.string()) })
export const totpEnable = (code: string) =>
  request('POST', '/api/v1/account/totp/enable', codesSchema, { code })
export const totpDisable = (password: string) =>
  request('POST', '/api/v1/account/totp/disable', accountSchema, { password })
export const newRecoveryCodes = (password: string) =>
  request('POST', '/api/v1/account/recovery-codes', codesSchema, { password })

const ceremonySchema = z.object({ id: z.string(), options: z.record(z.string(), z.unknown()) })

/** Registers a passkey: the server's options, the authenticator, the answer back. */
export async function addPasskey(
  name: string,
  create: (options: Record<string, unknown>) => Promise<Record<string, unknown>>,
) {
  const begin = await request('POST', '/api/v1/account/passkeys/begin', ceremonySchema)
  const credential = await create(begin.options)
  return request('POST', '/api/v1/account/passkeys/finish', accountSchema, {
    id: begin.id,
    name,
    credential,
  })
}

export const renamePasskey = (id: number, name: string) =>
  request('PATCH', `/api/v1/account/passkeys/${String(id)}`, accountSchema, { name })
export const removePasskey = (id: number) =>
  request('DELETE', `/api/v1/account/passkeys/${String(id)}`, accountSchema)

export const setStepUpOptOut = (optOut: boolean) =>
  request('PUT', '/api/v1/account/step-up', accountSchema, { optOut })

// Step-up: confirm it is you with the password, a code, or a passkey.
const verifiedSchema = z.object({ verifiedAt: z.string() })
export const stepUpWith = (body: { password?: string; code?: string }) =>
  request('POST', '/api/v1/auth/step-up', verifiedSchema, body)
export async function stepUpPasskey(
  sign: (options: Record<string, unknown>) => Promise<Record<string, unknown>>,
) {
  const begin = await request('POST', '/api/v1/auth/step-up/passkey/begin', ceremonySchema)
  const credential = await sign(begin.options)
  return request('POST', '/api/v1/auth/step-up/passkey/finish', verifiedSchema, {
    id: begin.id,
    credential,
  })
}
