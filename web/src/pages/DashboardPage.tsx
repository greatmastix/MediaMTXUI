import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { useState, type ReactNode } from 'react'

import { metricsHistoryQuery, rangeMs, rangeStepMs, type HistoryRange } from '@/api/history'
import { healthQuery, statusQuery } from '@/api/sidecar'
import { streamsQuery } from '@/api/streams'
import { RangePicker } from '@/components/RangePicker'
import { StatusPill } from '@/components/StatusPill'
import { TimeSeriesChart } from '@/components/TimeSeriesChart'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Skeleton } from '@/components/ui/skeleton'
import { useNow } from '@/hooks/useNow'
import { formatBitrate, formatCount, formatDuration } from '@/lib/format'
import { useLive } from '@/live/useLive'
import { listItems, type Sample } from '@/live/store'

export function DashboardPage() {
  const live = useLive()
  const now = useNow()
  const { data: health } = useQuery(healthQuery)
  const { data: st } = useQuery(statusQuery)
  const paths = listItems(live.lists, 'paths')
  const online = paths?.filter((p) => p.online) ?? []
  const last = live.samples.at(-1)

  let mediamtx = 'Checking…'
  if (live.status) {
    mediamtx = live.status.reachable ? 'Running' : 'Unreachable'
  }
  const started = live.info?.started ? Date.parse(live.info.started) : NaN

  return (
    <div className="space-y-6">
      <h1 className="font-heading text-2xl font-semibold">Dashboard</h1>

      <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
        <Stat title="MediaMTX" testId="mediamtx-status" value={mediamtx}>
          {live.status?.reachable && !Number.isNaN(started)
            ? `Version ${live.info?.version?.replace(/^v/, '') ?? 'unknown'}, up for ${formatDuration((now - started) / 1000)}`
            : ' '}
        </Stat>
        <Stat
          title="Paths"
          testId="stat-paths"
          value={paths ? `${formatCount(online.length)} online` : undefined}
        >
          {paths ? `${formatCount(paths.length)} in total` : ' '}
        </Stat>
        <Stat
          title="Audience"
          testId="stat-readers"
          value={
            last
              ? `${formatCount(last.readers)} ${last.readers === 1 ? 'reader' : 'readers'}`
              : undefined
          }
        >
          {last
            ? `${formatCount(last.clients)} ${last.clients === 1 ? 'client session' : 'client sessions'}`
            : ' '}
        </Stat>
        <Stat
          title="Traffic"
          testId="stat-traffic"
          value={last ? `${formatBitrate(last.outBps)} out` : undefined}
        >
          {last ? `${formatBitrate(last.inBps)} in` : ' '}
        </Stat>
      </div>

      <StreamsStrip />

      <DashboardCharts live={live.samples} now={now} />

      <div className="grid gap-4 lg:grid-cols-[2fr_1fr]">
        <Card>
          <CardHeader className="flex flex-row items-center justify-between">
            <CardTitle>
              <h2 className="section-title">Online paths</h2>
            </CardTitle>
            <Link to="/paths" className="text-sm underline-offset-4 hover:underline">
              All paths
            </Link>
          </CardHeader>
          <CardContent>
            {!paths ? (
              <Skeleton className="h-16 w-full" />
            ) : online.length === 0 ? (
              <p className="text-sm text-muted-foreground">
                Nothing is being published right now. Streams appear here as soon as a publisher
                connects.
              </p>
            ) : (
              <ul aria-label="Online paths" className="divide-y">
                {online.slice(0, 10).map((p) => {
                  const rate = live.rates.get(p.name ?? '')
                  return (
                    <li
                      key={p.name}
                      className="flex items-center justify-between gap-4 py-2 text-sm"
                    >
                      <Link
                        to="/paths/$"
                        params={{ _splat: p.name ?? '' }}
                        className="truncate font-medium underline-offset-4 hover:underline"
                        data-testid={`online-path-${p.name ?? ''}`}
                      >
                        {p.name}
                      </Link>
                      <span className="flex shrink-0 items-center gap-3 text-muted-foreground tabular-nums">
                        <span>{(p.tracks2 ?? []).map((t) => t.codec).join(', ')}</span>
                        <span>{formatCount(p.readers?.length ?? 0)} readers</span>
                        <span className="hidden sm:inline">{formatBitrate(rate?.inBps)}</span>
                      </span>
                    </li>
                  )
                })}
                {online.length > 10 && (
                  <li className="py-2 text-sm text-muted-foreground">
                    and {online.length - 10} more
                  </li>
                )}
              </ul>
            )}
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>
              <h2 className="section-title">Stack</h2>
            </CardTitle>
          </CardHeader>
          <CardContent>
            <dl className="grid grid-cols-[auto_1fr] gap-x-6 gap-y-2 text-sm">
              <dt className="text-muted-foreground">API protected</dt>
              <dd data-testid="api-protected">
                {st?.apiProtected === true ? (
                  'Yes'
                ) : st?.apiProtected === false ? (
                  <Badge variant="destructive">No</Badge>
                ) : (
                  'Unknown'
                )}
              </dd>
              <dt className="text-muted-foreground">Built for MediaMTX</dt>
              <dd data-testid="mediamtx-version">{health?.mediamtxVersion ?? '–'}</dd>
              <dt className="text-muted-foreground">Sidecar</dt>
              <dd data-testid="sidecar-version">{health?.version ?? '–'}</dd>
            </dl>
          </CardContent>
        </Card>
      </div>
    </div>
  )
}

function Stat({
  title,
  value,
  testId,
  children,
}: {
  title: string
  value: string | undefined
  testId: string
  children: ReactNode
}) {
  return (
    <Card size="sm">
      <CardHeader>
        <CardTitle className="text-[12.5px] font-normal text-muted-foreground">
          <h2>{title}</h2>
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-1">
        {value === undefined ? (
          <Skeleton className="h-8 w-32" />
        ) : (
          <p
            className="text-[26px] leading-tight font-semibold tracking-tight tabular-nums"
            data-testid={testId}
          >
            {value}
          </p>
        )}
        <p className="text-xs whitespace-pre text-muted-foreground">{children}</p>
      </CardContent>
    </Card>
  )
}

/** The streams, one click from their pages (the simple view of a path). */
function StreamsStrip() {
  const live = useLive()
  const { data: streams } = useQuery(streamsQuery)
  if (!streams?.length) return null
  return (
    <Card>
      <CardHeader className="flex flex-row items-center justify-between">
        <CardTitle>
          <h2 className="section-title">Streams</h2>
        </CardTitle>
        <Link to="/streams" className="text-sm underline-offset-4 hover:underline">
          All streams
        </Link>
      </CardHeader>
      <CardContent>
        <ul className="flex flex-wrap gap-2" aria-label="Streams">
          {streams.map((s) => {
            const p = live.lists.paths?.items.get(s.name)
            return (
              <li key={s.id}>
                <Link
                  to="/streams/$id"
                  params={{ id: String(s.id) }}
                  className="flex items-center gap-2 rounded-lg border px-3 py-1.5 text-sm hover:border-signal/60"
                >
                  <span className="font-medium">{s.title}</span>
                  {p?.online ? (
                    <StatusPill tone="good">
                      Live · {formatCount(p.readers?.length ?? 0)} watching
                    </StatusPill>
                  ) : (
                    <StatusPill tone="neutral">Offline</StatusPill>
                  )}
                </Link>
              </li>
            )
          })}
        </ul>
      </CardContent>
    </Card>
  )
}

/** Bandwidth and audience: the live hour from the event stream, or a longer range from the stored history. */
function DashboardCharts({ live, now }: { live: readonly Sample[]; now: number }) {
  const [range, setRange] = useState<HistoryRange>('1h')
  const stored = useQuery({
    ...metricsHistoryQuery(range === '1h' ? '24h' : range),
    enabled: range !== '1h',
  })
  const samples = range === '1h' ? live : (stored.data?.samples ?? [])
  const times = samples.map((s) => s.t)
  const windowMs = range === '1h' ? undefined : rangeMs(range)
  const gapMs = rangeStepMs[range] * 2.5
  return (
    <section className="space-y-3" aria-label="History">
      <div className="flex items-center justify-between gap-2">
        <h2 className="section-title">History</h2>
        <RangePicker value={range} onChange={setRange} />
      </div>
      <div className="grid gap-4 lg:grid-cols-2">
        <Card>
          <CardContent>
            <TimeSeriesChart
              title="Bandwidth"
              now={now}
              times={times}
              windowMs={windowMs}
              gapMs={gapMs}
              format={formatBitrate}
              series={[
                {
                  label: 'In',
                  className: 'text-chart-1',
                  value: (i) => samples[i]?.inBps ?? null,
                },
                {
                  label: 'Out',
                  className: 'text-chart-2',
                  value: (i) => samples[i]?.outBps ?? null,
                },
              ]}
            />
          </CardContent>
        </Card>
        <Card>
          <CardContent>
            <TimeSeriesChart
              title="Audience"
              now={now}
              times={times}
              windowMs={windowMs}
              gapMs={gapMs}
              format={(v) => formatCount(Math.round(v))}
              series={[
                {
                  label: 'Readers',
                  className: 'text-chart-1',
                  value: (i) => samples[i]?.readers ?? null,
                },
                {
                  label: 'Clients',
                  className: 'text-chart-2',
                  value: (i) => samples[i]?.clients ?? null,
                },
                {
                  label: 'Online paths',
                  className: 'text-chart-3',
                  value: (i) => samples[i]?.online ?? null,
                },
              ]}
            />
          </CardContent>
        </Card>
      </div>
    </section>
  )
}
