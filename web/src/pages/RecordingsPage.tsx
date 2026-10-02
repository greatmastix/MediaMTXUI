import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { HardDrive } from 'lucide-react'

import { ApiError } from '@/api/client'
import { recordingsQuery, releaseGuard, type RecordingsDisk } from '@/api/recordings'
import { sessionQuery, statusQuery } from '@/api/sidecar'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { useNow } from '@/hooks/useNow'
import { formatBytes, formatSince, formatTime } from '@/lib/format'
import { atLeast } from '@/lib/roles'

// Recordings: how full the recordings disk is (and the budget that keeps it from filling), and every path that has
// recordings, one click from its timeline.

export function RecordingsPage() {
  const q = useQuery(recordingsQuery)
  const now = useNow(60_000)
  return (
    <div className="max-w-5xl space-y-6">
      <div className="space-y-1">
        <h1 className="font-heading text-2xl font-semibold">Recordings</h1>
        <p className="text-sm text-muted-foreground">
          What MediaMTX recorded, per path: browse the timeline, play or download a stretch, delete
          what you do not need. Paths record when their record setting is on.
        </p>
      </div>
      {q.error && (
        <Alert variant="destructive" role="alert">
          <AlertDescription>
            {q.error instanceof ApiError ? q.error.message : 'The recordings cannot be listed.'}
          </AlertDescription>
        </Alert>
      )}
      {!q.data ? (
        <Skeleton className="h-40 w-full" />
      ) : (
        <>
          <Disk disk={q.data.disk} />
          {q.data.paths.length === 0 ? (
            <p className="rounded-lg border border-dashed p-8 text-center text-sm text-muted-foreground">
              Nothing recorded yet. Switch record on for a path (Configuration → Paths) and its
              recordings appear here.
            </p>
          ) : (
            <div className="rounded-lg border">
              <Table aria-label="Recorded paths">
                <TableHeader>
                  <TableRow>
                    <TableHead>Path</TableHead>
                    <TableHead className="text-right">Size</TableHead>
                    <TableHead className="hidden text-right sm:table-cell">Segments</TableHead>
                    <TableHead className="hidden md:table-cell">From</TableHead>
                    <TableHead>Latest</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {q.data.paths.map((p) => (
                    <TableRow key={p.name} data-testid={`recording-row-${p.name}`}>
                      <TableCell className="font-medium">
                        <Link
                          to="/recordings/$"
                          params={{ _splat: p.name }}
                          className="underline-offset-4 hover:underline"
                        >
                          {p.name}
                        </Link>
                      </TableCell>
                      <TableCell className="text-right tabular-nums">
                        {formatBytes(p.bytes)}
                      </TableCell>
                      <TableCell className="hidden text-right tabular-nums sm:table-cell">
                        {p.segments}
                      </TableCell>
                      <TableCell className="hidden md:table-cell">{formatTime(p.first)}</TableCell>
                      <TableCell>{formatSince(p.last, now)}</TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </div>
          )}
        </>
      )}
    </div>
  )
}

const pct = (part: number, whole: number) => (whole > 0 ? Math.min(100, (part / whole) * 100) : 0)

function Disk({ disk: d }: { disk: RecordingsDisk }) {
  const queryClient = useQueryClient()
  const { data: session } = useQuery(sessionQuery)
  const admin = atLeast(session?.user.role ?? 'viewer', 'admin')
  const release = useMutation({
    mutationFn: releaseGuard,
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: recordingsQuery.queryKey })
      void queryClient.invalidateQueries({ queryKey: statusQuery.queryKey })
    },
  })
  const used = d.total - d.free
  return (
    <section className="space-y-3 rounded-xl border bg-card p-4" aria-labelledby="disk-title">
      <h2 id="disk-title" className="flex items-center gap-2 font-medium">
        <HardDrive className="size-4 text-signal" aria-hidden /> Recordings disk
      </h2>
      {d.error ? (
        <p className="text-sm text-destructive">{d.error}</p>
      ) : (
        <>
          <svg
            className="h-3 w-full overflow-hidden rounded-full"
            viewBox="0 0 100 1"
            preserveAspectRatio="none"
            role="img"
            aria-label={`${formatBytes(d.recordings)} of recordings, ${formatBytes(d.free)} free of ${formatBytes(d.total)}`}
          >
            <rect width="100" height="1" className="fill-muted" />
            <rect width={pct(used, d.total)} height="1" className="fill-muted-foreground/40" />
            <rect width={pct(d.recordings, d.total)} height="1" className="fill-signal" />
          </svg>
          <dl className="grid gap-x-6 gap-y-1 text-sm sm:grid-cols-2" data-testid="recordings-disk">
            <div className="flex justify-between gap-2">
              <dt className="text-muted-foreground">Recordings</dt>
              <dd className="tabular-nums">
                {formatBytes(d.recordings)}
                {d.budget > 0 && ` of ${formatBytes(d.budget)} budget`}
              </dd>
            </div>
            <div className="flex justify-between gap-2">
              <dt className="text-muted-foreground">Free on the disk</dt>
              <dd className="tabular-nums">
                {formatBytes(d.free)} of {formatBytes(d.total)}
              </dd>
            </div>
            <div className="flex justify-between gap-2">
              <dt className="text-muted-foreground">Oldest go when</dt>
              <dd>
                {[
                  d.budget > 0 && `over ${formatBytes(d.budget)}`,
                  d.minFree > 0 && `under ${formatBytes(d.minFree)} free`,
                ]
                  .filter(Boolean)
                  .join(' or ') || 'never (no budget)'}
              </dd>
            </div>
            <div className="flex justify-between gap-2">
              <dt className="text-muted-foreground">Recording stops</dt>
              <dd>{d.critical > 0 ? `under ${formatBytes(d.critical)} free` : 'never'}</dd>
            </div>
          </dl>
        </>
      )}
      {d.guard && (
        <Alert variant="destructive" role="alert" data-testid="recordings-guard">
          <AlertTitle>Recording is switched off</AlertTitle>
          <AlertDescription>
            <p>
              Only {formatBytes(d.guard.free)} were free on {formatTime(d.guard.since)}, so
              recording was switched off for{' '}
              {[d.guard.defaults && 'every path by default', ...(d.guard.paths ?? [])]
                .filter(Boolean)
                .join(', ')}
              . Make room, then switch it back on.
            </p>
            {admin && (
              <Button
                size="sm"
                variant="outline"
                className="mt-2"
                disabled={release.isPending}
                onClick={() => {
                  release.mutate()
                }}
              >
                Switch recording back on
              </Button>
            )}
            {release.error && (
              <p className="mt-2">
                {release.error instanceof ApiError ? release.error.message : 'That did not work.'}
              </p>
            )}
          </AlertDescription>
        </Alert>
      )}
    </section>
  )
}
