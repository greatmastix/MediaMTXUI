import { z } from 'zod'

import { request } from './client'

// The audit log: read-only, newest first, filtered and paged by id.

export const auditEntrySchema = z.object({
  id: z.number(),
  at: z.string(),
  actor: z.string(),
  ip: z.string(),
  action: z.string(),
  target: z.string(),
  details: z.record(z.string(), z.unknown()),
})
export type AuditEntry = z.infer<typeof auditEntrySchema>

export interface AuditFilter {
  actor?: string
  action?: string
  target?: string
  from?: string // ISO
  to?: string
}

export function auditParams(f: AuditFilter, extra: Record<string, string> = {}) {
  const q = new URLSearchParams()
  for (const [k, v] of Object.entries({ ...f, ...extra })) {
    if (v) q.set(k, v)
  }
  return q.toString()
}

export const fetchAudit = (f: AuditFilter, before?: number) =>
  request(
    'GET',
    `/api/v1/audit?${auditParams(f, before ? { before: String(before) } : {})}`,
    z.array(auditEntrySchema),
  )

export const auditExportURL = (f: AuditFilter, format: 'csv' | 'json') =>
  `/api/v1/audit/export?${auditParams(f, { format })}`
