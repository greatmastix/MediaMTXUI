import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Archive, Download, KeyRound, RotateCcw, Trash2, Upload } from 'lucide-react'
import { useEffect, useState } from 'react'

import {
  backupDownloadURL,
  backupsQuery,
  cancelRestore,
  checkBackup,
  createBackup,
  deleteBackup,
  restoreBackup,
  setBackupPassphrase,
  setBackupSchedule,
  uploadBackup,
  type BackupInfo,
  type Backups,
  type RestorePreview,
} from '@/api/backups'
import { ApiError } from '@/api/client'
import { sessionQuery } from '@/api/sidecar'
import { fieldClass } from '@/components/config/SettingsForm'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button, buttonVariants } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { formatBytes, formatTime } from '@/lib/format'
import { atLeast } from '@/lib/roles'

// Backups (admins): the passphrase, the schedule, the kept backups (download, restore, delete),
// and a restore from an uploaded file. A restore shows what it would do first, then restarts the sidecar.

const kindLabel: Record<BackupInfo['kind'], string> = {
  scheduled: 'Scheduled',
  manual: 'Made by hand',
  'before-restore': 'Before a restore',
  upload: 'Uploaded',
}

const errorText = (e: unknown, fallback: string) => (e instanceof Error ? e.message : fallback)

export function BackupsPage() {
  const { data: session } = useQuery(sessionQuery)
  const admin = atLeast(session?.user.role ?? 'viewer', 'admin')
  const backups = useQuery({ ...backupsQuery, enabled: admin })
  const [restarting, setRestarting] = useState(false)

  if (!admin) {
    return (
      <div className="space-y-2">
        <h1 className="font-heading text-2xl font-semibold">Backups</h1>
        <p className="text-sm text-muted-foreground">Backups are for admins.</p>
      </div>
    )
  }
  if (restarting) return <Restarting />
  return (
    <div className="space-y-6">
      <div className="space-y-1">
        <h1 className="flex items-center gap-2 font-heading text-2xl font-semibold">
          <Archive className="size-6 text-signal" aria-hidden /> Backups
        </h1>
        <p className="text-sm text-muted-foreground">
          Everything needed to set this server up again: people, streams and their keys, the
          configuration and its history, holding clips, the charts&apos; history and the audit log.
          Not the recordings. Backups are encrypted: only the passphrase opens them, so keep it
          somewhere safe. They are kept in <code>backups/</code> on the server; download one now and
          then to keep a copy elsewhere.
        </p>
      </div>
      {backups.error ? (
        <Alert variant="destructive" role="alert">
          <AlertDescription>{backups.error.message}</AlertDescription>
        </Alert>
      ) : !backups.data ? (
        <Skeleton className="h-60 w-full" />
      ) : (
        <>
          <Passphrase configured={backups.data.configured} />
          {backups.data.configured && <ScheduleForm data={backups.data} />}
          {backups.data.staged ? (
            <Preview
              preview={backups.data.staged}
              onRestarting={() => {
                setRestarting(true)
              }}
            />
          ) : (
            <BackupList data={backups.data} />
          )}
        </>
      )}
    </div>
  )
}

function Section({
  title,
  children,
  testId,
}: {
  title: string
  children: React.ReactNode
  testId?: string
}) {
  const id = `backups-${title.toLowerCase().replace(/[^a-z]+/g, '-')}`
  return (
    <section
      className="space-y-3 rounded-xl border bg-card p-4"
      aria-labelledby={id}
      data-testid={testId}
    >
      <h2 id={id} className="font-medium">
        {title}
      </h2>
      {children}
    </section>
  )
}

function Passphrase({ configured }: { configured: boolean }) {
  const queryClient = useQueryClient()
  const [open, setOpen] = useState(!configured)
  const [pass, setPass] = useState('')
  const [again, setAgain] = useState('')
  const set = useMutation({
    mutationFn: () => setBackupPassphrase(pass),
    onSuccess: async () => {
      setPass('')
      setAgain('')
      setOpen(false)
      await queryClient.invalidateQueries({ queryKey: backupsQuery.queryKey })
    },
  })
  return (
    <Section title="Passphrase">
      <p className="text-sm text-muted-foreground">
        {configured
          ? 'Set. Backups made from now on open with the new one if you change it; older backups keep the passphrase they were made with.'
          : 'Backups start once a passphrase is set. Without it nobody can open a backup, this server included: if it is lost, so are the backups.'}
      </p>
      {open ? (
        <form
          className="flex flex-wrap items-end gap-2"
          onSubmit={(e) => {
            e.preventDefault()
            set.mutate()
          }}
        >
          <label className="space-y-1 text-sm">
            <span className="font-medium">Passphrase</span>
            <input
              type="password"
              autoComplete="new-password"
              className={fieldClass}
              value={pass}
              onChange={(e) => {
                setPass(e.target.value)
              }}
            />
          </label>
          <label className="space-y-1 text-sm">
            <span className="font-medium">Again</span>
            <input
              type="password"
              autoComplete="new-password"
              className={fieldClass}
              value={again}
              onChange={(e) => {
                setAgain(e.target.value)
              }}
            />
          </label>
          <Button type="submit" disabled={set.isPending || pass.length < 12 || pass !== again}>
            <KeyRound /> {configured ? 'Change passphrase' : 'Set passphrase'}
          </Button>
          {configured && (
            <Button
              type="button"
              variant="ghost"
              onClick={() => {
                setOpen(false)
              }}
            >
              Cancel
            </Button>
          )}
          <span className="w-full text-xs text-muted-foreground">
            At least 12 characters.
            {again !== '' && pass !== again && ' The two differ.'}
          </span>
        </form>
      ) : (
        <Button
          size="sm"
          variant="outline"
          onClick={() => {
            setOpen(true)
          }}
        >
          Change passphrase
        </Button>
      )}
      {set.error && (
        <p role="alert" className="text-sm text-destructive">
          {errorText(set.error, 'The passphrase was not set.')}
        </p>
      )}
    </Section>
  )
}

/** HH:MM in UTC as the browser's local time, for the hint. */
function localTime(utc: string) {
  const [h, m] = utc.split(':').map(Number)
  if (h === undefined || m === undefined || Number.isNaN(h) || Number.isNaN(m)) return ''
  const d = new Date()
  d.setUTCHours(h, m, 0, 0)
  return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
}

function ScheduleForm({ data }: { data: Backups }) {
  const queryClient = useQueryClient()
  const [draft, setDraft] = useState(data.schedule)
  const save = useMutation({
    mutationFn: () => setBackupSchedule(draft),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: backupsQuery.queryKey })
    },
  })
  const dirty = JSON.stringify(draft) !== JSON.stringify(data.schedule)
  return (
    <Section title="Schedule">
      <form
        className="flex flex-wrap items-end gap-3"
        onSubmit={(e) => {
          e.preventDefault()
          save.mutate()
        }}
      >
        <label className="flex items-center gap-2 text-sm">
          <input
            type="checkbox"
            checked={draft.enabled}
            onChange={(e) => {
              setDraft({ ...draft, enabled: e.target.checked })
            }}
          />
          <span className="font-medium">Back up every day</span>
        </label>
        <label className="space-y-1 text-sm">
          <span className="font-medium">At (UTC)</span>
          <input
            type="time"
            className={fieldClass}
            value={draft.time}
            onChange={(e) => {
              setDraft({ ...draft, time: e.target.value })
            }}
          />
        </label>
        <label className="space-y-1 text-sm">
          <span className="font-medium">Keep the newest</span>
          <input
            type="number"
            min={1}
            max={365}
            className={`${fieldClass} w-24`}
            value={draft.keep}
            onChange={(e) => {
              setDraft({ ...draft, keep: Number(e.target.value) })
            }}
          />
        </label>
        <Button type="submit" disabled={!dirty || save.isPending}>
          Save
        </Button>
        <span className="w-full text-xs text-muted-foreground">
          {draft.time && `That is ${localTime(draft.time)} your time. `}
          Backups made by hand are kept until you delete them.
        </span>
      </form>
      {save.error && (
        <p role="alert" className="text-sm text-destructive">
          {errorText(save.error, 'The schedule was not saved.')}
        </p>
      )}
      {data.last && (
        <p className="text-sm" data-testid="backup-last">
          {data.last.ok ? 'Last backup ' : 'The last backup failed '}
          {formatTime(data.last.at)}
          {data.last.message ? `: ${data.last.message}` : '.'}
        </p>
      )}
    </Section>
  )
}

function BackupList({ data }: { data: Backups }) {
  const queryClient = useQueryClient()
  const [checking, setChecking] = useState<string | null>(null)
  const refresh = () => queryClient.invalidateQueries({ queryKey: backupsQuery.queryKey })
  const make = useMutation({ mutationFn: createBackup, onSuccess: refresh })
  const remove = useMutation({ mutationFn: deleteBackup, onSuccess: refresh })
  const upload = useMutation({
    mutationFn: uploadBackup,
    onSuccess: async (b) => {
      await refresh()
      setChecking(b.name)
    },
  })
  return (
    <Section title="Kept backups" testId="backup-list">
      <div className="flex flex-wrap gap-2">
        <Button
          disabled={!data.configured || make.isPending}
          onClick={() => {
            make.mutate()
          }}
        >
          <Archive /> {make.isPending ? 'Backing up…' : 'Back up now'}
        </Button>
        <label className={buttonVariants({ variant: 'outline' })}>
          <Upload /> {upload.isPending ? 'Uploading…' : 'Restore from a file…'}
          <input
            type="file"
            accept=".mtxbackup"
            className="sr-only"
            onChange={(e) => {
              const f = e.target.files?.[0]
              e.target.value = ''
              if (!f) return
              if (f.size > data.maxUploadBytes) {
                upload.reset()
                return
              }
              upload.mutate(f)
            }}
          />
        </label>
      </div>
      {(make.error ?? remove.error ?? upload.error) && (
        <p role="alert" className="text-sm text-destructive">
          {errorText(make.error ?? remove.error ?? upload.error, 'That did not work.')}
        </p>
      )}
      {data.backups.length === 0 ? (
        <p className="rounded-lg border border-dashed p-6 text-center text-sm text-muted-foreground">
          No backups yet.
        </p>
      ) : (
        <div className="rounded-lg border">
          <Table aria-label="Backups">
            <TableHeader>
              <TableRow>
                <TableHead>Made</TableHead>
                <TableHead>Kind</TableHead>
                <TableHead className="hidden sm:table-cell">Size</TableHead>
                <TableHead className="hidden md:table-cell">From</TableHead>
                <TableHead />
              </TableRow>
            </TableHeader>
            <TableBody>
              {data.backups.map((b) => (
                <TableRow key={b.name} data-testid="backup-row">
                  <TableCell className="whitespace-nowrap tabular-nums">
                    {formatTime(b.created)}
                  </TableCell>
                  <TableCell>
                    <Badge variant="outline">{kindLabel[b.kind]}</Badge>
                  </TableCell>
                  <TableCell className="hidden tabular-nums sm:table-cell">
                    {formatBytes(b.size)}
                  </TableCell>
                  <TableCell className="hidden text-muted-foreground md:table-cell">
                    {b.host || '–'} {b.version && `(${b.version})`}
                  </TableCell>
                  <TableCell className="text-right">
                    <div className="flex flex-wrap justify-end gap-1">
                      <a
                        className={buttonVariants({ size: 'sm', variant: 'outline' })}
                        href={backupDownloadURL(b.name)}
                        download
                        aria-label={`Download the backup of ${formatTime(b.created)}`}
                      >
                        <Download />
                      </a>
                      <Button
                        size="sm"
                        variant="outline"
                        onClick={() => {
                          setChecking(checking === b.name ? null : b.name)
                        }}
                      >
                        <RotateCcw /> Restore…
                      </Button>
                      <Button
                        size="sm"
                        variant="outline"
                        aria-label={`Delete the backup of ${formatTime(b.created)}`}
                        disabled={remove.isPending}
                        onClick={() => {
                          if (window.confirm('Delete this backup? It cannot be brought back.')) {
                            remove.mutate(b.name)
                          }
                        }}
                      >
                        <Trash2 />
                      </Button>
                    </div>
                    {checking === b.name && (
                      <Check
                        name={b.name}
                        onCancel={() => {
                          setChecking(null)
                        }}
                      />
                    )}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      )}
    </Section>
  )
}

/** The passphrase for a restore's preview. */
function Check({ name, onCancel }: { name: string; onCancel: () => void }) {
  const queryClient = useQueryClient()
  const [pass, setPass] = useState('')
  const check = useMutation({
    mutationFn: () => checkBackup(name, pass),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: backupsQuery.queryKey })
    },
  })
  return (
    <form
      className="mt-2 flex flex-wrap items-end justify-end gap-2 text-left"
      aria-label="Check the backup"
      onSubmit={(e) => {
        e.preventDefault()
        check.mutate()
      }}
    >
      <label className="space-y-1 text-sm">
        <span className="font-medium">The backup&apos;s passphrase</span>
        <input
          type="password"
          autoComplete="off"
          autoFocus
          className={fieldClass}
          value={pass}
          onChange={(e) => {
            setPass(e.target.value)
          }}
        />
      </label>
      <Button type="submit" size="sm" disabled={!pass || check.isPending}>
        {check.isPending ? 'Checking…' : 'Check it'}
      </Button>
      <Button type="button" size="sm" variant="ghost" onClick={onCancel}>
        Cancel
      </Button>
      {check.error && (
        <p role="alert" className="w-full text-right text-sm text-destructive">
          {check.error instanceof ApiError && check.error.kind === 'passphrase'
            ? 'That passphrase does not open this backup.'
            : errorText(check.error, 'The backup could not be checked.')}
        </p>
      )}
    </form>
  )
}

function diff(from: string[], to: string[]) {
  return { gone: from.filter((x) => !to.includes(x)), back: to.filter((x) => !from.includes(x)) }
}

function Preview({
  preview: p,
  onRestarting,
}: {
  preview: RestorePreview
  onRestarting: () => void
}) {
  const queryClient = useQueryClient()
  const users = diff(p.currentUsers, p.database.users)
  const streams = diff(p.currentStreams, p.database.streams ?? [])
  const cancel = useMutation({
    mutationFn: cancelRestore,
    onSuccess: () => queryClient.invalidateQueries({ queryKey: backupsQuery.queryKey }),
  })
  const restore = useMutation({
    mutationFn: () => restoreBackup(p.name),
    onSuccess: onRestarting,
  })
  return (
    <Section title="Restore this backup?" testId="restore-preview">
      <p className="text-sm">
        The backup of <strong>{formatTime(p.created)}</strong> (sidecar {p.version}, MediaMTX{' '}
        {p.mediamtx}) opened and passed every check. Restoring it puts this server back to then:
      </p>
      <ul className="list-disc space-y-1 pl-5 text-sm">
        <li data-testid="preview-users">
          {p.database.users.length} people ({p.database.users.join(', ')})
          {users.gone.length > 0 && `; gone again: ${users.gone.join(', ')}`}
          {users.back.length > 0 && `; back: ${users.back.join(', ')}`}
        </li>
        <li data-testid="preview-streams">
          {(p.database.streams ?? []).length} streams
          {streams.gone.length > 0 && `; gone again: ${streams.gone.join(', ')}`}
          {streams.back.length > 0 && `; back: ${streams.back.join(', ')}`}
        </li>
        <li>
          {p.database.credentials} stream credentials, {p.database.snapshots} configuration
          versions, {p.clips} holding clips
        </li>
        <li>
          {p.configChanges
            ? 'mediamtx.yml changes: paths whose settings differ restart, which drops their viewers for a moment.'
            : 'mediamtx.yml stays as it is.'}
        </li>
        <li>
          The charts&apos; history ({Math.round(p.database.historyMinutes / 60)} hours) and the
          audit log ({p.database.auditEntries} entries) are the backup&apos;s; this restore is added
          to it.
        </li>
      </ul>
      <Alert role="note">
        <AlertDescription>
          A backup of the current state is made first. The sidecar then restarts (a few seconds;
          MediaMTX keeps running) and everyone signs in again, with the backup&apos;s passwords.
        </AlertDescription>
      </Alert>
      <div className="flex flex-wrap gap-2">
        <Button
          variant="destructive"
          disabled={restore.isPending}
          onClick={() => {
            restore.mutate()
          }}
        >
          <RotateCcw /> {restore.isPending ? 'Restoring…' : 'Restore'}
        </Button>
        <Button
          variant="outline"
          disabled={cancel.isPending}
          onClick={() => {
            cancel.mutate()
          }}
        >
          Cancel
        </Button>
      </div>
      {(restore.error ?? cancel.error) && (
        <p role="alert" className="text-sm text-destructive">
          {errorText(restore.error ?? cancel.error, 'That did not work.')}
        </p>
      )}
    </Section>
  )
}

/** After a restore: waits for the sidecar to stop and come back, then goes to sign-in. */
function Restarting() {
  useEffect(() => {
    let stop = false
    const sleep = (ms: number) =>
      new Promise((r) => {
        setTimeout(r, ms)
      })
    const poll = async () => {
      await sleep(2500) // the old process answers for half a second more
      while (!stop) {
        try {
          const res = await fetch('/api/v1/setup', { cache: 'no-store' })
          if (res.ok) {
            window.location.assign('/login')
            return
          }
        } catch {
          // not back yet
        }
        await sleep(1000)
      }
    }
    void poll()
    return () => {
      stop = true
    }
  }, [])
  return (
    <div className="space-y-2 py-16 text-center" role="status" data-testid="restarting">
      <RotateCcw className="mx-auto size-8 animate-spin text-signal" aria-hidden />
      <p className="font-heading text-xl font-semibold">Restoring…</p>
      <p className="text-sm text-muted-foreground">
        The server restarts with the backup. This page goes to sign-in when it is back.
      </p>
    </div>
  )
}
