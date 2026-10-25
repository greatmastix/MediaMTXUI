import { queryOptions } from '@tanstack/react-query'
import { z } from 'zod'

import { request } from './client'

// New releases (admins): the sidecar asks GitHub once a day; each admin can skip a version or be reminded in a week.

export const updatesSchema = z.object({
  current: z.string(),
  checking: z.boolean(),
  /** Why the check cannot be switched on here ("" when it can). */
  locked: z.string(),
  latest: z.string().optional(),
  /** The release's page on GitHub, as the sidecar checked it. */
  page: z.string().optional(),
  published: z.string().optional(),
  checkedAt: z.string().optional(),
  error: z.string().optional(),
  available: z.boolean(),
  show: z.boolean(),
  skipped: z.string().optional(),
  snoozedUntil: z.string().optional(),
})
export type Updates = z.infer<typeof updatesSchema>

export const updatesQuery = queryOptions({
  queryKey: ['updates'],
  queryFn: ({ signal }) => request('GET', '/api/v1/updates', updatesSchema, undefined, signal),
  staleTime: 15 * 60_000,
})

export const setUpdateCheck = (check: boolean) =>
  request('PUT', '/api/v1/updates/settings', updatesSchema, { check })

export const dismissUpdate = (action: 'skip' | 'later') =>
  request('PUT', '/api/v1/updates/dismissal', updatesSchema, { action })

/** The upgrade guide, as it is for that version. */
export const upgradeGuide = (version: string) =>
  `https://github.com/greatmastix/MediaMTXUI/blob/v${version}/docs/upgrading.md`
