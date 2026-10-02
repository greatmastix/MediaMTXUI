import { queryOptions } from '@tanstack/react-query'
import { z } from 'zod'

import { request } from './client'

// Recordings: MediaMTX records, the sidecar lists, plays, exports and deletes, and keeps the disk
// within its budget. Exports are plain GET URLs (path and times only, no secrets), so a <video> or a link can use them
// with the session cookie.

const guardSchema = z.object({
  since: z.string(),
  free: z.number(),
  paths: z.array(z.string()).nullable(),
  defaults: z.boolean(),
})

export const recordingsDiskSchema = z.object({
  recordings: z.number(),
  free: z.number(),
  total: z.number(),
  budget: z.number(),
  minFree: z.number(),
  critical: z.number(),
  measuredAt: z.string().nullable(),
  error: z.string().optional(),
  guard: guardSchema.nullable(),
})
export type RecordingsDisk = z.infer<typeof recordingsDiskSchema>

export const recordingPathSchema = z.object({
  name: z.string(),
  segments: z.number(),
  first: z.string(),
  last: z.string(),
  bytes: z.number(),
})
export type RecordingPath = z.infer<typeof recordingPathSchema>

export const recordingsQuery = queryOptions({
  queryKey: ['recordings'],
  queryFn: ({ signal }) =>
    request(
      'GET',
      '/api/v1/recordings',
      z.object({ disk: recordingsDiskSchema, paths: z.array(recordingPathSchema) }),
      undefined,
      signal,
    ),
  refetchInterval: 15_000,
})

export const spanSchema = z.object({ start: z.string(), duration: z.number() })
export type Span = z.infer<typeof spanSchema>

/** Stretches recorded without a gap, within [start, end). */
export const spansQuery = (path: string, start: Date, end: Date) =>
  queryOptions({
    queryKey: ['recordings', path, 'spans', start.toISOString(), end.toISOString()],
    queryFn: ({ signal }) =>
      request(
        'GET',
        `/api/v1/recordings/spans?${new URLSearchParams({ path, start: start.toISOString(), end: end.toISOString() }).toString()}`,
        z.array(spanSchema),
        undefined,
        signal,
      ),
    refetchInterval: 15_000,
  })

/** The start of every segment (the unit of deletion). */
export const segmentsQuery = (path: string) =>
  queryOptions({
    queryKey: ['recordings', path, 'segments'],
    queryFn: ({ signal }) =>
      request(
        'GET',
        `/api/v1/recordings/segments?${new URLSearchParams({ path }).toString()}`,
        z.array(z.object({ start: z.string() })),
        undefined,
        signal,
      ),
    refetchInterval: 15_000,
  })

/** Where to fetch a range: fmp4 plays in a <video>, mp4 (download) saves as a file. */
export function exportURL(path: string, start: Date, seconds: number, format: 'mp4' | 'fmp4') {
  const q = new URLSearchParams({
    path,
    start: start.toISOString(),
    duration: seconds.toFixed(3),
    format,
  })
  if (format === 'mp4') q.set('download', '1')
  return `/api/v1/recordings/export?${q.toString()}`
}

export const deleteSegments = (path: string, starts: string[]) =>
  request(
    'POST',
    '/api/v1/recordings/delete',
    z.object({ deleted: z.number(), failed: z.number() }),
    { path, starts },
  )

export const releaseGuard = () =>
  request('POST', '/api/v1/recordings/guard/release', recordingsDiskSchema)
