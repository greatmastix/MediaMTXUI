import { queryOptions } from '@tanstack/react-query'
import { z } from 'zod'

import { sessionViewSchema } from './account'
import { request, requestNoContent } from './client'

// People (admins): the UI's users and invitations. An invited person joins with a one-time code at /join.

export const personSchema = z.object({
  id: z.number(),
  username: z.string(),
  role: z.enum(['admin', 'operator', 'viewer', 'streamer']),
  disabled: z.boolean(),
  pending: z.boolean(),
  joinExpires: z.string().nullable(),
  streams: z.array(z.object({ id: z.number(), name: z.string() })),
  createdAt: z.string(),
  totp: z.boolean(),
  passkeys: z.number(),
})
export type Person = z.infer<typeof personSchema>

export const invitationSchema = z.object({
  user: personSchema,
  joinCode: z.string(),
  expires: z.string(),
})
export type Invitation = z.infer<typeof invitationSchema>

export const peopleQuery = queryOptions({
  queryKey: ['people'],
  queryFn: ({ signal }) =>
    request('GET', '/api/v1/users', z.array(personSchema), undefined, signal),
})

export const invite = (body: { username: string; role: Person['role'] }) =>
  request('POST', '/api/v1/users', invitationSchema, body)

export const newJoinCode = (id: number) =>
  request('POST', `/api/v1/users/${String(id)}/join-code`, invitationSchema)

export const setRole = (id: number, role: Person['role']) =>
  requestNoContent('PATCH', `/api/v1/users/${String(id)}`, { role })

export const deletePerson = (id: number) =>
  requestNoContent('DELETE', `/api/v1/users/${String(id)}`)

export const setDisabled = (id: number, disabled: boolean) =>
  requestNoContent('PATCH', `/api/v1/users/${String(id)}`, { disabled })

/** Signs a person out everywhere (yourself: everywhere else). */
export const endSessions = (id: number) =>
  requestNoContent('DELETE', `/api/v1/users/${String(id)}/sessions`)

/** Takes away a person's authenticator app, recovery codes and passkeys (a lost phone or key). */
export const resetSecondFactor = (id: number) =>
  requestNoContent('DELETE', `/api/v1/users/${String(id)}/second-factor`)

export const personSessionsQuery = (id: number) =>
  queryOptions({
    queryKey: ['people', id, 'sessions'],
    queryFn: ({ signal }) =>
      request(
        'GET',
        `/api/v1/users/${String(id)}/sessions`,
        z.array(sessionViewSchema),
        undefined,
        signal,
      ),
  })
