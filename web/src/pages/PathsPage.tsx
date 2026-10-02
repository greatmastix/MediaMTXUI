import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { Plus, Search, Wand2 } from 'lucide-react'
import { useState } from 'react'

import { sessionQuery } from '@/api/sidecar'
import { buttonVariants } from '@/components/ui/button'

import { StatusPill } from '@/components/StatusPill'
import { Input } from '@/components/ui/input'
import { Skeleton } from '@/components/ui/skeleton'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { formatBitrate, formatBytes, formatCount } from '@/lib/format'
import { describeType } from '@/lib/protocols'
import { atLeast } from '@/lib/roles'
import { useLive } from '@/live/useLive'
import { listItems } from '@/live/store'

export function PathsPage() {
  const live = useLive()
  const { data: session } = useQuery(sessionQuery)
  const admin = atLeast(session?.user.role ?? 'streamer', 'admin')
  const [filter, setFilter] = useState('')
  const all = listItems(live.lists, 'paths')
  const q = filter.trim().toLowerCase()
  const paths = all?.filter((p) => !q || (p.name ?? '').toLowerCase().includes(q))

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-end justify-between gap-4">
        <div className="space-y-1">
          <h1 className="font-heading text-2xl font-semibold">Paths</h1>
          <p className="text-sm text-muted-foreground">
            Every path MediaMTX knows right now: configured paths, and paths that exist while a
            publisher is connected.
          </p>
        </div>
        <div className="flex w-full flex-wrap items-center gap-2 sm:w-auto">
          {admin && (
            <>
              <Link to="/config/quick" className={buttonVariants({ variant: 'outline' })}>
                <Wand2 aria-hidden /> Quick setup
              </Link>
              <Link to="/config/new-path" className={buttonVariants()}>
                <Plus aria-hidden /> New path
              </Link>
            </>
          )}
          <div className="relative w-full sm:w-64">
            <Search
              className="absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground"
              aria-hidden
            />
            <Input
              type="search"
              aria-label="Filter paths by name"
              placeholder="Filter by name"
              className="pl-8"
              value={filter}
              onChange={(e) => {
                setFilter(e.target.value)
              }}
            />
          </div>
        </div>
      </div>

      {!paths ? (
        <Skeleton className="h-32 w-full" />
      ) : paths.length === 0 ? (
        <p className="rounded-lg border border-dashed p-8 text-center text-sm text-muted-foreground">
          {all?.length
            ? 'No path matches the filter.'
            : 'No paths yet. Publish a stream and it appears here.'}
        </p>
      ) : (
        <div className="rounded-lg border">
          <Table aria-label="Paths">
            <TableHeader>
              <TableRow>
                <TableHead>Name</TableHead>
                <TableHead>State</TableHead>
                <TableHead className="hidden md:table-cell">Source</TableHead>
                <TableHead className="hidden lg:table-cell">Tracks</TableHead>
                <TableHead className="text-right">Readers</TableHead>
                <TableHead className="text-right">In</TableHead>
                <TableHead className="hidden text-right sm:table-cell">Out</TableHead>
                <TableHead className="hidden text-right xl:table-cell">Received</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {paths.map((p) => {
                const name = p.name ?? ''
                const rate = live.rates.get(name)
                return (
                  <TableRow key={name} data-testid={`path-row-${name}`}>
                    <TableCell className="max-w-64 truncate font-medium">
                      <Link
                        to="/paths/$"
                        params={{ _splat: name }}
                        className="underline-offset-4 hover:underline"
                      >
                        {name}
                      </Link>
                    </TableCell>
                    <TableCell>
                      <StatusPill tone={p.online ? 'good' : 'neutral'}>
                        {p.online ? 'Online' : 'Offline'}
                      </StatusPill>
                    </TableCell>
                    <TableCell className="hidden md:table-cell">
                      {describeType(p.source?.type)}
                    </TableCell>
                    <TableCell className="hidden lg:table-cell">
                      {(p.tracks2 ?? []).map((t) => t.codec).join(', ') || '–'}
                    </TableCell>
                    <TableCell className="text-right tabular-nums">
                      {formatCount(p.readers?.length ?? 0)}
                    </TableCell>
                    <TableCell className="text-right tabular-nums">
                      {formatBitrate(rate?.inBps)}
                    </TableCell>
                    <TableCell className="hidden text-right tabular-nums sm:table-cell">
                      {formatBitrate(rate?.outBps)}
                    </TableCell>
                    <TableCell className="hidden text-right tabular-nums xl:table-cell">
                      {formatBytes(p.inboundBytes)}
                    </TableCell>
                  </TableRow>
                )
              })}
            </TableBody>
          </Table>
        </div>
      )}
      {live.lists.paths?.truncated && (
        <p className="text-sm text-muted-foreground">
          MediaMTX reports more paths than the UI mirrors; only the first 20,000 are shown.
        </p>
      )}
    </div>
  )
}
