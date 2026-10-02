import { queryOptions } from '@tanstack/react-query'
import { z } from 'zod'

import { request, requestNoContent } from './client'
import type { HistoryRange } from './history'

// Streams: a MediaMTX path as a page for whoever streams to it. Streamers see their own; viewers
// and up see all; owners, operators and admins manage one; admins create, delete and pick owners.

const keyInfoSchema = z.object({
  name: z.string(),
  createdAt: z.string(),
  lastUsedAt: z.string().nullable(),
})

export const streamSchema = z.object({
  id: z.number(),
  name: z.string(),
  title: z.string(),
  target: z.string(),
  maxReaders: z.number(),
  record: z.boolean(),
  public: z.boolean(),
  owner: z.object({ id: z.number(), username: z.string() }).nullable(),
  keys: z.object({ publish: keyInfoSchema.optional(), playback: keyInfoSchema.optional() }),
  createdAt: z.string(),
  createdBy: z.string(),
  canManage: z.boolean(),
  canAdmin: z.boolean(),
  ingest: z.object({
    host: z.string(),
    rtsp: z.number().optional(),
    rtmp: z.number().optional(),
    srt: z.number().optional(),
    hls: z.boolean(),
    webrtc: z.boolean(),
  }),
  audio: z.enum(['aac', 'opus']),
  holding: z.enum(['', 'builtin', 'file']),
  clips: z.object({ aac: z.boolean(), opus: z.boolean() }),
  format: z.enum(['720p50', '720p60', '1080p50', '1080p60']),
})
export type Stream = z.infer<typeof streamSchema>
export type KeyKind = 'publish' | 'playback'

export const streamKeySchema = z.object({
  kind: z.enum(['publish', 'playback']),
  name: z.string(),
  secret: z.string(),
})
export type StreamKey = z.infer<typeof streamKeySchema>

export const pathSampleSchema = z.object({
  t: z.number(),
  inBps: z.number(),
  outBps: z.number(),
  readers: z.number(),
})
export type PathSample = z.infer<typeof pathSampleSchema>

export const streamsQuery = queryOptions({
  queryKey: ['streams'],
  queryFn: ({ signal }) =>
    request('GET', '/api/v1/streams', z.array(streamSchema), undefined, signal),
})

export const streamQuery = (id: number) =>
  queryOptions({
    queryKey: ['streams', id],
    queryFn: ({ signal }) =>
      request('GET', `/api/v1/streams/${String(id)}`, streamSchema, undefined, signal),
  })

/** A stream's history: the last hour's 5-second points (loaded once; the event stream's rates extend them), or
 * a longer range from the stored minutes, refreshed every minute. */
export const streamHistoryQuery = (id: number, range: HistoryRange = '1h') =>
  queryOptions({
    queryKey: ['streams', id, 'history', range],
    queryFn: ({ signal }) =>
      request(
        'GET',
        `/api/v1/streams/${String(id)}/history?range=${range}`,
        z.array(pathSampleSchema),
        undefined,
        signal,
      ),
    staleTime: range === '1h' ? Infinity : 60_000,
    refetchInterval: range === '1h' ? false : 60_000,
  })

export const createStream = (s: {
  name: string
  title: string
  ownerId: number | null
  target: string
}) => request('POST', '/api/v1/streams', streamSchema, s)

export const updateStream = (
  id: number,
  patch: {
    title?: string
    target?: string
    maxReaders?: number
    record?: boolean
    ownerId?: number
    public?: boolean
    holding?: Stream['holding']
    audio?: Stream['audio']
    format?: Stream['format']
  },
) => request('PATCH', `/api/v1/streams/${String(id)}`, streamSchema, patch)

/**
 * Uploads a holding clip (an MP4) for a stream and switches its holding screen to it. The clip was made for that audio
 * and format, which become the stream's.
 */
export const uploadHoldingClip = (
  id: number,
  clip: Blob,
  made: { audio: Stream['audio']; format: Stream['format'] },
) =>
  request(
    'PUT',
    `/api/v1/streams/${String(id)}/holding/clip?${new URLSearchParams(made).toString()}`,
    streamSchema,
    new Blob([clip], { type: 'video/mp4' }),
  )

export const deleteStream = (id: number) =>
  requestNoContent('DELETE', `/api/v1/streams/${String(id)}`)

export const revealKey = (id: number, kind: KeyKind) =>
  request('POST', `/api/v1/streams/${String(id)}/keys/${kind}/reveal`, streamKeySchema)

export const regenerateKey = (id: number, kind: KeyKind) =>
  request('POST', `/api/v1/streams/${String(id)}/keys/${kind}/regenerate`, streamKeySchema)

/** Keeps the stream's publishing ports open to this browser's address while its page is open (automatic exposure). */
export const leaseStream = (id: number) =>
  request(
    'POST',
    `/api/v1/streams/${String(id)}/lease`,
    z.object({
      leased: z.boolean(),
      off: z.boolean().optional(),
      address: z.string(),
      until: z.string().optional(),
      reason: z.string().optional(),
    }),
  )

export const disconnectStream = (id: number) =>
  request('POST', `/api/v1/streams/${String(id)}/disconnect`, z.object({ kicked: z.number() }))

/** What a public stream's watch link shows; anyone may ask. */
export const publicStreamQuery = (name: string) =>
  queryOptions({
    queryKey: ['public-stream', name],
    queryFn: ({ signal }) =>
      request(
        'GET',
        `/api/v1/public/streams/${name.split('/').map(encodeURIComponent).join('/')}`,
        z.object({
          name: z.string(),
          title: z.string(),
          live: z.boolean(),
          available: z.boolean(),
        }),
        undefined,
        signal,
      ),
    refetchInterval: 10_000, // no event stream without an account: the live state is polled
  })

// Forwarding: the stream re-streamed to other platforms. The platform's key goes in once and never
// comes back from the server.

export const forwardSchema = z.object({
  id: z.number(),
  provider: z.string(),
  label: z.string(),
  enabled: z.boolean(),
  state: z.enum(['off', 'idle', 'forwarding', 'error', 'missing', 'unknown']),
  lastError: z.string().optional(),
  outboundBytes: z.number(),
  createdAt: z.string(),
  createdBy: z.string(),
})
export type Forward = z.infer<typeof forwardSchema>

export const forwardsQuery = (id: number) =>
  queryOptions({
    queryKey: ['streams', id, 'forwards'],
    queryFn: ({ signal }) =>
      request(
        'GET',
        `/api/v1/streams/${String(id)}/forwards`,
        z.array(forwardSchema),
        undefined,
        signal,
      ),
    refetchInterval: 5000, // the state comes from MediaMTX, which has no event for it
  })

export const createForward = (
  id: number,
  f: { provider: string; server: string; key: string; enabled: boolean },
) => request('POST', `/api/v1/streams/${String(id)}/forwards`, forwardSchema, f)

export const setForwardEnabled = (id: number, fid: number, enabled: boolean) =>
  request('PATCH', `/api/v1/streams/${String(id)}/forwards/${String(fid)}`, forwardSchema, {
    enabled,
  })

export const deleteForward = (id: number, fid: number) =>
  requestNoContent('DELETE', `/api/v1/streams/${String(id)}/forwards/${String(fid)}`)

// Guest keys: time-limited keys for one stream. The secret comes back once, from the creation.

export const guestKeySchema = z.object({
  id: z.number(),
  name: z.string(),
  kind: z.enum(['publish', 'read']),
  label: z.string(),
  state: z.enum(['active', 'expired', 'revoked']),
  expiresAt: z.string().nullable(),
  lastUsedAt: z.string().nullable(),
  createdAt: z.string(),
  createdBy: z.string(),
})
export type GuestKey = z.infer<typeof guestKeySchema>

export const guestKeysQuery = (id: number) =>
  queryOptions({
    queryKey: ['streams', id, 'guest-keys'],
    queryFn: ({ signal }) =>
      request(
        'GET',
        `/api/v1/streams/${String(id)}/guest-keys`,
        z.array(guestKeySchema),
        undefined,
        signal,
      ),
  })

export const createGuestKey = (
  id: number,
  g: { kind: GuestKey['kind']; label: string; hours: number },
) =>
  request(
    'POST',
    `/api/v1/streams/${String(id)}/guest-keys`,
    z.object({ guest: guestKeySchema, secret: z.string() }),
    g,
  )

export const revokeGuestKey = (id: number, kid: number) =>
  request('DELETE', `/api/v1/streams/${String(id)}/guest-keys/${String(kid)}`, guestKeySchema)

/** What MediaMTX's log said about the stream's encoder lately: refused (tracks), B-frames. Newest first. */
export const streamNotesQuery = (id: number) =>
  queryOptions({
    queryKey: ['streams', id, 'notes'],
    queryFn: ({ signal }) =>
      request(
        'GET',
        `/api/v1/streams/${String(id)}/notes`,
        z.array(z.object({ kind: z.string(), message: z.string(), at: z.string() })),
        undefined,
        signal,
      ),
    refetchInterval: 5000,
  })
