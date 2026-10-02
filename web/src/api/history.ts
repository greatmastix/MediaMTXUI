import { queryOptions } from '@tanstack/react-query'
import { z } from 'zod'

import { sampleSchema } from '@/live/store'

import { request } from './client'

// The charts' ranges: the last hour is the event stream's 5-second samples; the longer ones come
// from the sidecar's stored minutes, in buckets of a minute, ten minutes or an hour.

export const historyRanges = [
  { value: '1h', label: '1 h', ms: 3_600_000 },
  { value: '24h', label: '24 h', ms: 86_400_000 },
  { value: '7d', label: '7 d', ms: 7 * 86_400_000 },
  { value: '30d', label: '30 d', ms: 30 * 86_400_000 },
] as const

export type HistoryRange = (typeof historyRanges)[number]['value']

/** How long a range is, in milliseconds. */
export const rangeMs = (r: HistoryRange) =>
  historyRanges.find((x) => x.value === r)?.ms ?? 3_600_000

/** The spacing of a range's points: samples further apart than about two of these are a gap. */
export const rangeStepMs: Record<HistoryRange, number> = {
  '1h': 5_000,
  '24h': 60_000,
  '7d': 600_000,
  '30d': 3_600_000,
}

const historySchema = z.object({ intervalSeconds: z.number(), samples: z.array(sampleSchema) })

/** The dashboard's history over a longer range, refreshed every minute. */
export const metricsHistoryQuery = (range: Exclude<HistoryRange, '1h'>) =>
  queryOptions({
    queryKey: ['metrics', 'history', range],
    queryFn: ({ signal }) =>
      request('GET', `/api/v1/metrics/history?range=${range}`, historySchema, undefined, signal),
    staleTime: 60_000,
    refetchInterval: 60_000,
  })
