import { queryOptions } from '@tanstack/react-query'
import { z } from 'zod'

import { request, requestNoContent } from './client'

// Backups: encrypted with a passphrase, made daily and on demand, kept on the server and
// downloadable; restored from a kept or uploaded file after a preview.

export const backupInfoSchema = z.object({
  name: z.string(),
  kind: z.enum(['scheduled', 'manual', 'before-restore', 'upload']),
  size: z.number(),
  created: z.string(),
  host: z.string(),
  version: z.string(),
})
export type BackupInfo = z.infer<typeof backupInfoSchema>

export const scheduleSchema = z.object({ enabled: z.boolean(), time: z.string(), keep: z.number() })
export type Schedule = z.infer<typeof scheduleSchema>

export const previewSchema = z.object({
  name: z.string(),
  created: z.string(),
  version: z.string(),
  mediamtx: z.string(),
  database: z.object({
    schema: z.number(),
    users: z.array(z.string()),
    admins: z.number(),
    streams: z.array(z.string()).nullable(),
    credentials: z.number(),
    snapshots: z.number(),
    historyMinutes: z.number(),
    auditEntries: z.number(),
  }),
  currentUsers: z.array(z.string()),
  currentStreams: z.array(z.string()),
  clips: z.number(),
  configChanges: z.boolean(),
  expiresAt: z.string(),
})
export type RestorePreview = z.infer<typeof previewSchema>

export const backupsSchema = z.object({
  configured: z.boolean(),
  schedule: scheduleSchema,
  last: z
    .object({
      at: z.string(),
      ok: z.boolean(),
      kind: z.string(),
      name: z.string().optional(),
      message: z.string().optional(),
    })
    .nullable(),
  backups: z.array(backupInfoSchema),
  maxUploadBytes: z.number(),
  staged: previewSchema.nullable(),
})
export type Backups = z.infer<typeof backupsSchema>

export const backupsQuery = queryOptions({
  queryKey: ['backups'],
  queryFn: ({ signal }) => request('GET', '/api/v1/backups', backupsSchema, undefined, signal),
})

export const createBackup = () => request('POST', '/api/v1/backups', backupInfoSchema)

export const setBackupPassphrase = (passphrase: string) =>
  requestNoContent('PUT', '/api/v1/backups/passphrase', { passphrase })

export const setBackupSchedule = (s: Schedule) =>
  request('PUT', '/api/v1/backups/schedule', scheduleSchema, s)

export const deleteBackup = (name: string) =>
  requestNoContent('DELETE', `/api/v1/backups/${encodeURIComponent(name)}`)

export const uploadBackup = (file: Blob) =>
  request(
    'POST',
    '/api/v1/backups/upload',
    backupInfoSchema,
    new Blob([file], { type: 'application/octet-stream' }),
  )

export const checkBackup = (name: string, passphrase: string) =>
  request('POST', `/api/v1/backups/${encodeURIComponent(name)}/check`, previewSchema, {
    passphrase,
  })

export const cancelRestore = () => requestNoContent('DELETE', '/api/v1/backups/restore')

export const restoreBackup = (name: string) =>
  request(
    'POST',
    `/api/v1/backups/${encodeURIComponent(name)}/restore`,
    z.object({ restarting: z.boolean(), before: z.string().optional() }),
  )

export const backupDownloadURL = (name: string) =>
  `/api/v1/backups/${encodeURIComponent(name)}/download`
