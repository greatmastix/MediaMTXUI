import { useQuery } from '@tanstack/react-query'
import { Link, useNavigate } from '@tanstack/react-router'
import { useState } from 'react'

import { ApiError } from '@/api/client'
import {
  describeWrite,
  restoreSnapshot,
  snapshotQuery,
  snapshotsQuery,
  type Snapshot,
} from '@/api/config'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { diffLines, hunks } from '@/lib/diff'
import { formatTime } from '@/lib/format'
import { cn } from '@/lib/utils'

import { useConfigNote, type SavedNoteState } from './useSavedNote'
import { useConfig } from './useConfig'

/** Every version of mediamtx.yml, what each changed, and restoring an old one (as a new version). */
export function HistoryPage({ selected }: { selected?: number }) {
  const list = useQuery(snapshotsQuery)
  const { config } = useConfig()
  const note = useConfigNote()
  if (!list.data || !config) return <Skeleton className="h-64 w-full" />
  const latest = list.data[0]
  const current = selected ?? latest?.id
  return (
    <div className="grid gap-4 lg:grid-cols-[20rem_minmax(0,1fr)]">
      <ol aria-label="Versions" className="space-y-1">
        {list.data.map((s) => (
          <li key={s.id}>
            <Link
              to="/config/history"
              search={{ id: s.id }}
              aria-current={s.id === current ? 'true' : undefined}
              className="block rounded-lg border border-transparent px-3 py-2 text-sm hover:bg-accent aria-[current=true]:border-border aria-[current=true]:bg-card"
            >
              <span className="flex items-baseline justify-between gap-2">
                <span className="font-medium">Version {s.id}</span>
                <span className="text-xs text-muted-foreground">{formatTime(s.at)}</span>
              </span>
              <span className="block truncate text-xs text-muted-foreground">
                {s.author}: {s.reason}
                {s.id === latest?.id && s.sha256 === config.sha256 ? ' (current)' : ''}
              </span>
            </Link>
          </li>
        ))}
      </ol>
      {current !== undefined && (
        <VersionDetail
          key={current}
          snapshot={list.data.find((s) => s.id === current)}
          id={current}
          isCurrent={current === latest?.id && latest.sha256 === config.sha256}
          note={note}
        />
      )}
    </div>
  )
}

function VersionDetail({
  id,
  snapshot,
  isCurrent,
  note,
}: {
  id: number
  snapshot?: Snapshot
  isCurrent: boolean
  note: SavedNoteState['show']
}) {
  const { refresh } = useConfig()
  const navigate = useNavigate()
  const full = useQuery(snapshotQuery(id))
  const parentId = full.data?.parentId ?? snapshot?.parentId ?? null
  const parent = useQuery({ ...snapshotQuery(parentId ?? 0), enabled: parentId !== null })
  const [confirm, setConfirm] = useState(false)
  const [error, setError] = useState<string | null>(null)
  if (!full.data || (parentId !== null && !parent.data)) return <Skeleton className="h-64 w-full" />
  const lines = diffLines(parent.data?.content ?? '', full.data.content ?? '')
  const shown = parentId === null ? [] : hunks(lines)

  const restore = async () => {
    setError(null)
    try {
      const res = await restoreSnapshot(id)
      note(res.changed ? describeWrite(res, 'Restored') : 'That is already the current file.')
      setConfirm(false)
      await refresh()
      if (res.snapshot) await navigate({ to: '/config/history', search: { id: res.snapshot.id } })
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'The version could not be restored.')
    }
  }

  return (
    <section aria-label={`Version ${String(id)}`} className="min-w-0 space-y-3">
      <div className="flex flex-wrap items-center gap-3">
        <h2 className="font-heading text-lg font-semibold">Version {id}</h2>
        <span className="text-sm text-muted-foreground">
          {full.data.author}, {formatTime(full.data.at)}: {full.data.reason}
        </span>
        <div className="flex-1" />
        {!isCurrent &&
          (confirm ? (
            <span className="inline-flex gap-2">
              <Button
                size="sm"
                onClick={() => {
                  void restore()
                }}
              >
                Restore version {id}
              </Button>
              <Button
                size="sm"
                variant="outline"
                onClick={() => {
                  setConfirm(false)
                }}
              >
                Cancel
              </Button>
            </span>
          ) : (
            <Button
              size="sm"
              variant="outline"
              onClick={() => {
                setConfirm(true)
              }}
            >
              Restore…
            </Button>
          ))}
      </div>
      {error && (
        <Alert variant="destructive" role="alert">
          <AlertDescription className="whitespace-pre-wrap">{error}</AlertDescription>
        </Alert>
      )}
      {parentId === null ? (
        <p className="text-sm text-muted-foreground">The first version: nothing to compare with.</p>
      ) : shown.length === 0 ? (
        <p className="text-sm text-muted-foreground">Identical to version {parentId}.</p>
      ) : (
        <div
          className="overflow-x-auto rounded-lg border bg-card font-mono text-xs"
          data-testid="diff"
          // Long lines scroll sideways: keyboard users reach the region too.
          tabIndex={0}
          role="region"
          aria-label={`Changes since version ${String(parentId)}`}
        >
          <p className="border-b px-3 py-1.5 text-muted-foreground">
            Changes since version {parentId}
          </p>
          {shown.map((h, i) => (
            <div key={i} className={cn(i > 0 && 'border-t border-dashed')}>
              {h.lines.map((l, j) => (
                <div
                  key={j}
                  className={cn(
                    'grid grid-cols-[3rem_3rem_1rem_1fr] whitespace-pre',
                    l.kind === 'add' && 'bg-good/15',
                    l.kind === 'del' && 'bg-destructive/15',
                  )}
                >
                  <span className="pr-2 text-right text-muted-foreground select-none">
                    {l.oldNo ?? ''}
                  </span>
                  <span className="pr-2 text-right text-muted-foreground select-none">
                    {l.newNo ?? ''}
                  </span>
                  <span
                    className="select-none"
                    aria-label={
                      l.kind === 'add' ? 'added' : l.kind === 'del' ? 'removed' : undefined
                    }
                  >
                    {l.kind === 'add' ? '+' : l.kind === 'del' ? '−' : ' '}
                  </span>
                  <span>{l.text}</span>
                </div>
              ))}
            </div>
          ))}
        </div>
      )}
    </section>
  )
}
