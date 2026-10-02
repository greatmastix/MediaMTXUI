import { queryOptions } from '@tanstack/react-query'
import { z } from 'zod'

import { request } from './client'

// Exposure control (admin only): which stream ports the internet may reach. The sidecar writes a wish; the host
// helper mtx-portgate applies it within the operator's policy and reports what it actually did. The status streams
// as the "exposure" extra of the event stream; GET adds the caller's address as the sidecar sees it.

const portStatusSchema = z.object({
  proto: z.string(),
  port: z.number(),
  state: z.enum(['open', 'closed', 'error']),
  sources: z.array(z.string()).optional(),
  until: z.string().nullable().optional(),
  error: z.string().optional(),
})

const statusSchema = z.object({
  rev: z.number(),
  updated: z.string(),
  driver: z.string(),
  ports: z.record(z.string(), portStatusSchema),
  limits: z.object({
    allowAnySource: z.boolean(),
    maxTTLAnySource: z.string(),
    maxTTL: z.string(),
    permanentOK: z.boolean(),
    maxSources: z.number(),
    minPrefixV4: z.number(),
    minPrefixV6: z.number(),
  }),
  error: z.string().optional(),
  closedAll: z.string().nullable().optional(),
})

const desiredSchema = z.object({
  rev: z.number(),
  want: z.record(
    z.string(),
    z.object({ sources: z.array(z.string()).nullable(), until: z.string().nullable() }),
  ),
})

const autoRulesSchema = z.object({
  publish: z.boolean(),
  remember: z.boolean(),
  viewers: z.boolean(),
})
export type AutoRules = z.infer<typeof autoRulesSchema>

export const exposureSchema = z.object({
  installed: z.boolean(),
  status: statusSchema.optional(),
  desired: desiredSchema.optional(),
  manual: desiredSchema.optional(),
  pending: z.boolean(),
  auto: z
    .array(
      z.object({ port: z.string(), source: z.string(), until: z.string(), reason: z.string() }),
    )
    .optional(),
  rules: autoRulesSchema.optional(),
})
export type Exposure = z.infer<typeof exposureSchema>
export type PortStatus = z.infer<typeof portStatusSchema>

export const exposureQuery = queryOptions({
  queryKey: ['exposure'],
  queryFn: ({ signal }) =>
    request(
      'GET',
      '/api/v1/exposure',
      exposureSchema.extend({ clientIP: z.string() }),
      undefined,
      signal,
    ),
})

export interface OpenRequest {
  sources: string[]
  myAddress: boolean
  hours: number
  permanent: boolean
}

export const openPort = (id: string, r: OpenRequest) =>
  request('PUT', `/api/v1/exposure/${encodeURIComponent(id)}`, exposureSchema, r)

export const closePort = (id: string) =>
  request('DELETE', `/api/v1/exposure/${encodeURIComponent(id)}`, exposureSchema)

export const closeAll = () => request('POST', '/api/v1/exposure/close-all', exposureSchema)

export const setAutoRules = (rules: AutoRules) =>
  request('PUT', '/api/v1/exposure/auto', autoRulesSchema, rules)

/** Hours in a Go duration string such as "12h0m0s" (what the helper reports its limits in). */
export function durationHours(d: string): number {
  let h = 0
  for (const [, n, unit] of d.matchAll(/([\d.]+)(h|m|s|ms|us|µs|ns)/g)) {
    const v = Number(n)
    h += unit === 'h' ? v : unit === 'm' ? v / 60 : unit === 's' ? v / 3600 : 0
  }
  return h
}
