import { queryOptions } from '@tanstack/react-query'
import { z } from 'zod'

import { request } from './client'

// The log viewer: MediaMTX's log (with its rotated copies) and the sidecar's own recent
// lines, searched, tailed live over SSE, and MediaMTX's downloaded.

export const logLineSchema = z.object({
  t: z.number(),
  source: z.enum(['mediamtx', 'sidecar']),
  level: z.enum(['debug', 'info', 'warn', 'error']),
  text: z.string(),
})
export type LogLine = z.infer<typeof logLineSchema>

export const logLinesSchema = z.object({ lines: z.array(logLineSchema), more: z.boolean() })

export interface LogFilter {
  source: LogLine['source']
  level: '' | LogLine['level']
  q: string
}

export function logParams(f: LogFilter, extra: Record<string, string> = {}) {
  const p = new URLSearchParams({ source: f.source, ...extra })
  if (f.level) p.set('level', f.level)
  if (f.q) p.set('q', f.q)
  return p.toString()
}

export const logSearchQuery = (f: LogFilter) =>
  queryOptions({
    queryKey: ['logs', f],
    queryFn: ({ signal }) =>
      request(
        'GET',
        `/api/v1/logs?${logParams(f, { limit: '2000' })}`,
        logLinesSchema,
        undefined,
        signal,
      ),
    staleTime: 0,
  })

export const logStreamURL = (f: LogFilter) => `/api/v1/logs/stream?${logParams(f)}`

export const logDownloadURL = '/api/v1/logs/download'
