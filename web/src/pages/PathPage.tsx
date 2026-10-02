import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { ChevronLeft } from 'lucide-react'
import type { ReactNode } from 'react'

import { sessionQuery } from '@/api/sidecar'
import { streamsQuery } from '@/api/streams'
import { Player } from '@/components/Player'
import { StatusPill } from '@/components/StatusPill'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
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
import { formatBitrate, formatBytes, formatCount, formatSince, formatTime } from '@/lib/format'
import { describeType, kindOfType, protocolOfKind } from '@/lib/protocols'
import { atLeast } from '@/lib/roles'
import { useLive } from '@/live/useLive'

export function PathPage({ name }: { name: string }) {
  const live = useLive()
  const now = useNow()
  const { data: session } = useQuery(sessionQuery)
  const operator = atLeast(session?.user.role ?? 'viewer', 'operator')
  const paths = live.lists.paths
  const p = paths?.items.get(name)
  const rate = live.rates.get(name)
  const muxer = live.lists.hlsMuxers?.items.get(name)
  const stream = useQuery(streamsQuery).data?.find((s) => s.name === name)

  const back = (
    <Link
      to="/paths"
      className="inline-flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground"
    >
      <ChevronLeft className="size-4" aria-hidden />
      Paths
    </Link>
  )
  if (!paths) {
    return (
      <div className="space-y-4">
        {back}
        <Skeleton className="h-40 w-full" />
      </div>
    )
  }
  if (!p) {
    return (
      <div className="space-y-4">
        {back}
        <h1 className="font-heading text-2xl font-semibold break-all">{name}</h1>
        <p
          className="rounded-lg border border-dashed p-8 text-center text-sm text-muted-foreground"
          data-testid="path-missing"
        >
          MediaMTX has no path by this name right now. Paths that are not configured exist only
          while a publisher is connected; this page updates by itself when one connects.
        </p>
      </div>
    )
  }

  const sourceKind = kindOfType(p.source?.type)
  return (
    <div className="space-y-6">
      {back}
      <div className="flex flex-wrap items-center gap-3">
        <h1 className="font-heading text-2xl font-semibold break-all">{name}</h1>
        <StatusPill tone={p.online ? 'good' : 'neutral'} data-testid="path-state">
          {p.online ? 'Online' : 'Offline'}
        </StatusPill>
        {stream && (
          <Link
            to="/streams/$id"
            params={{ id: String(stream.id) }}
            className="text-sm text-muted-foreground underline-offset-4 hover:text-foreground hover:underline"
          >
            Stream page
          </Link>
        )}
      </div>

      {p.online && (
        <div className="max-w-3xl space-y-2">
          <Player path={name} stats />
          <Link
            to="/watch"
            search={{ path: name }}
            className="text-sm text-muted-foreground underline-offset-4 hover:text-foreground hover:underline"
          >
            Open in the multi-view
          </Link>
        </div>
      )}

      <div className="grid gap-4 lg:grid-cols-2">
        <Card>
          <CardHeader>
            <CardTitle>
              <h2 className="section-title">Overview</h2>
            </CardTitle>
          </CardHeader>
          <CardContent>
            <dl className="grid grid-cols-[auto_1fr] gap-x-6 gap-y-2 text-sm">
              <Row label="Configuration">{p.confName ?? '–'}</Row>
              <Row label="Source">
                {p.source ? (
                  <ItemLink kind={sourceKind} id={p.source.id} operator={operator}>
                    {describeType(p.source.type)}
                  </ItemLink>
                ) : (
                  'none'
                )}
              </Row>
              <Row label="Online since">
                {p.onlineTime
                  ? `${formatTime(p.onlineTime)} (${formatSince(p.onlineTime, now)})`
                  : '–'}
              </Row>
              <Row label="Available since">
                {p.availableTime ? formatTime(p.availableTime) : '–'}
              </Row>
              <Row label="Incoming">
                {formatBitrate(rate?.inBps)}, {formatBytes(p.inboundBytes)} in total
              </Row>
              <Row label="Outgoing">
                {formatBitrate(rate?.outBps)}, {formatBytes(p.outboundBytes)} in total
              </Row>
              <Row label="Frames in error">{formatCount(p.inboundFramesInError ?? 0)}</Row>
              {muxer && (
                <Row label="HLS muxer">
                  active, last request {formatSince(muxer.lastRequest, now)}
                </Row>
              )}
            </dl>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>
              <h2 className="section-title">Tracks</h2>
            </CardTitle>
          </CardHeader>
          <CardContent>
            {(p.tracks2 ?? []).length === 0 ? (
              <p className="text-sm text-muted-foreground">
                No tracks: nothing is being published.
              </p>
            ) : (
              <ul className="space-y-3 text-sm" aria-label="Tracks">
                {(p.tracks2 ?? []).map((t, i) => (
                  <li key={i} className="space-y-1">
                    <p className="font-medium">{t.codec}</p>
                    <p className="text-xs break-all text-muted-foreground">
                      {Object.entries(t.codecProps ?? {})
                        .map(([k, v]) => `${k} ${String(v)}`)
                        .join(' · ') || 'no codec details'}
                    </p>
                  </li>
                ))}
              </ul>
            )}
          </CardContent>
        </Card>
      </div>

      <Card>
        <CardHeader>
          <CardTitle>
            <h2 className="section-title">Readers ({formatCount(p.readers?.length ?? 0)})</h2>
          </CardTitle>
        </CardHeader>
        <CardContent>
          {(p.readers ?? []).length === 0 ? (
            <p className="text-sm text-muted-foreground">Nobody is reading this path.</p>
          ) : (
            <Table aria-label="Readers">
              <TableHeader>
                <TableRow>
                  <TableHead>Type</TableHead>
                  <TableHead>ID</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {(p.readers ?? []).map((r) => (
                  <TableRow key={`${r.type ?? ''}-${r.id ?? ''}`}>
                    <TableCell>{describeType(r.type)}</TableCell>
                    <TableCell className="font-mono text-xs">
                      <ItemLink kind={kindOfType(r.type)} id={r.id} operator={operator}>
                        {r.id ?? '–'}
                      </ItemLink>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>
    </div>
  )
}

function Row({ label, children }: { label: string; children: ReactNode }) {
  return (
    <>
      <dt className="text-muted-foreground">{label}</dt>
      <dd className="min-w-0 break-words">{children}</dd>
    </>
  )
}

/** Links a session or connection to its detail on the connections page, for operators. */
function ItemLink({
  kind,
  id,
  operator,
  children,
}: {
  kind: ReturnType<typeof kindOfType>
  id: string | undefined
  operator: boolean
  children: ReactNode
}) {
  const protocol = kind ? protocolOfKind(kind) : undefined
  if (!operator || !kind || !protocol || !id) return <>{children}</>
  return (
    <Link
      to="/connections/$protocol"
      params={{ protocol: protocol.id }}
      search={{ kind, id }}
      className="underline underline-offset-4"
    >
      {children}
    </Link>
  )
}
