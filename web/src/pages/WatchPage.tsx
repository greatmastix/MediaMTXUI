import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { z } from 'zod'

import { ApiError, request, requestNoContent } from '@/api/client'
import { streamsQuery } from '@/api/streams'
import { fieldClass } from '@/components/config/SettingsForm'
import { Player } from '@/components/Player'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { cn } from '@/lib/utils'
import { listItems } from '@/live/store'
import { useLive } from '@/live/useLive'

// The live view: one to nine players in a grid, each on a path of its own choosing. Layouts are saved per user on
// the server (version 1: columns and tiles), so they follow the user from browser to browser; the one on screen is
// saved as it changes and comes back next time. A tile may name a path that is not live: it waits and starts playing
// when the path goes live.

const layoutSchema = z.object({
  version: z.literal(1),
  columns: z.number().int().min(1).max(3),
  tiles: z.array(z.string().nullable()),
})
type Layout = z.infer<typeof layoutSchema>

const savedSchema = z.array(
  z.object({ name: z.string(), layout: layoutSchema, updatedAt: z.string() }),
)

const layoutsQuery = {
  queryKey: ['layouts'],
  queryFn: ({ signal }: { signal: AbortSignal }) =>
    request('GET', '/api/v1/layouts', savedSchema, undefined, signal),
}

const currentQuery = {
  queryKey: ['watch', 'current'],
  queryFn: ({ signal }: { signal: AbortSignal }) =>
    request('GET', '/api/v1/watch/current', layoutSchema.nullable(), undefined, signal),
  staleTime: Infinity,
}

const tilesFor = (columns: number) => columns * columns

function fit(tiles: (string | null)[], n: number) {
  return Array.from({ length: n }, (_, i) => tiles[i] ?? null)
}

export function WatchPage({
  initialPath,
  policy,
}: {
  initialPath?: string
  policy?: RTCIceTransportPolicy
}) {
  const live = useLive()
  const queryClient = useQueryClient()
  const saved = useQuery(layoutsQuery)
  const current = useQuery({ ...currentQuery, enabled: !initialPath })
  const streams = useQuery(streamsQuery)
  const [layout, setLayout] = useState<Layout>({
    version: 1,
    columns: initialPath ? 1 : 2,
    tiles: initialPath ? [initialPath] : [],
  })
  // The layout from last time, once it has loaded (unless the address asked for one path).
  const [restored, setRestored] = useState(Boolean(initialPath))
  if (!restored && current.isFetched) {
    setRestored(true)
    if (current.data) setLayout(current.data)
  }
  const keep = useMutation({
    mutationFn: (l: Layout) => requestNoContent('PUT', '/api/v1/watch/current', l),
  })
  /** Shows a layout and keeps it as the current one. */
  const change = (next: Layout) => {
    const kept = { ...next, tiles: fit(next.tiles, tilesFor(next.columns)) }
    setLayout(next)
    // The cached copy too: coming back to this page restores from the cache, not the server.
    queryClient.setQueryData(currentQuery.queryKey, kept)
    keep.mutate(kept)
  }
  const [name, setName] = useState('')
  const [stats, setStats] = useState(false)
  const [message, setMessage] = useState<string | null>(null)
  const listed = listItems(live.lists, 'paths') ?? []
  const online = listed.filter((p) => p.online).map((p) => p.name ?? '')
  // Something plays: the stream, or its holding screen while nobody streams.
  const playable = listed
    .filter((p) => Boolean(p.online) || Boolean(p.available))
    .map((p) => p.name ?? '')
  // Every path a tile can name: what MediaMTX lists (live or not) and every stream.
  const known = [
    ...new Set([...listed.map((p) => p.name ?? ''), ...(streams.data ?? []).map((s) => s.name)]),
  ]
    .filter(Boolean)
    .sort((a, b) => Number(online.includes(b)) - Number(online.includes(a)) || a.localeCompare(b))
  const n = tilesFor(layout.columns)
  const tiles = fit(layout.tiles, n)

  const save = useMutation({
    mutationFn: () =>
      requestNoContent('PUT', `/api/v1/layouts/${encodeURIComponent(name.trim())}`, {
        ...layout,
        tiles,
      }),
    onSuccess: async () => {
      setMessage(`Saved as ${name.trim()}.`)
      await queryClient.invalidateQueries({ queryKey: ['layouts'] })
    },
    onError: (err) => {
      setMessage(err instanceof ApiError ? err.message : 'The layout could not be saved.')
    },
  })
  const remove = useMutation({
    mutationFn: (n: string) =>
      requestNoContent('DELETE', `/api/v1/layouts/${encodeURIComponent(n)}`),
    onSuccess: async (_r, n) => {
      setMessage(`Deleted ${n}.`)
      setName('')
      await queryClient.invalidateQueries({ queryKey: ['layouts'] })
    },
  })

  return (
    <div className="space-y-4">
      <h1 className="font-heading text-2xl font-semibold">Watch</h1>
      <div className="flex flex-wrap items-end gap-3">
        <div className="space-y-1">
          <label htmlFor="watch-columns" className="block text-xs text-muted-foreground">
            Grid
          </label>
          <select
            id="watch-columns"
            className={cn(fieldClass, 'w-28')}
            value={layout.columns}
            onChange={(e) => {
              change({ ...layout, columns: Number(e.target.value), tiles })
            }}
          >
            <option value={1}>1 player</option>
            <option value={2}>2 × 2</option>
            <option value={3}>3 × 3</option>
          </select>
        </div>
        <div className="space-y-1">
          <label htmlFor="watch-layout" className="block text-xs text-muted-foreground">
            Saved layout
          </label>
          <select
            id="watch-layout"
            className={cn(fieldClass, 'w-48')}
            value=""
            onChange={(e) => {
              const l = saved.data?.find((x) => x.name === e.target.value)
              if (l) {
                change(l.layout)
                setName(l.name)
                setMessage(null)
              }
            }}
          >
            <option value="">{saved.data?.length ? 'Open a layout…' : 'No saved layouts'}</option>
            {saved.data?.map((l) => (
              <option key={l.name} value={l.name}>
                {l.name}
              </option>
            ))}
          </select>
        </div>
        <div className="space-y-1">
          <label htmlFor="watch-name" className="block text-xs text-muted-foreground">
            Name
          </label>
          <input
            id="watch-name"
            className={cn(fieldClass, 'w-48')}
            value={name}
            placeholder="Front of house"
            onChange={(e) => {
              setName(e.target.value)
            }}
          />
        </div>
        <Button
          variant="outline"
          disabled={!name.trim() || save.isPending}
          onClick={() => {
            save.mutate()
          }}
        >
          Save layout
        </Button>
        {saved.data?.some((l) => l.name === name.trim()) && (
          <Button
            variant="ghost"
            onClick={() => {
              remove.mutate(name.trim())
            }}
          >
            Delete
          </Button>
        )}
        <label className="flex items-center gap-2 pb-1.5 text-sm">
          <Checkbox
            checked={stats}
            onCheckedChange={(v) => {
              setStats(v)
            }}
          />
          Stats
        </label>
      </div>
      {message && (
        <p role="status" className="text-sm" data-testid="watch-message">
          {message}
        </p>
      )}
      <div
        className={cn(
          'grid gap-2',
          layout.columns === 2 && 'md:grid-cols-2',
          layout.columns === 3 && 'md:grid-cols-3',
        )}
        data-testid="watch-grid"
      >
        {tiles.map((path, i) => (
          <div key={i} className="space-y-1.5">
            <select
              aria-label={`Stream for tile ${String(i + 1)}`}
              className={cn(fieldClass, 'text-xs')}
              value={path ?? ''}
              onChange={(e) => {
                const next = [...tiles]
                next[i] = e.target.value || null
                change({ ...layout, tiles: next })
              }}
            >
              <option value="">Empty</option>
              {[...new Set([...known, ...(path ? [path] : [])])].map((p) => (
                <option key={p} value={p}>
                  {p}
                  {online.includes(p)
                    ? ''
                    : playable.includes(p)
                      ? ' (holding screen)'
                      : ' (offline)'}
                </option>
              ))}
            </select>
            {path && playable.includes(path) ? (
              <Player key={path} path={path} stats={stats} policy={policy} />
            ) : path ? (
              <div
                className="grid aspect-video place-items-center rounded-lg border border-dashed p-4 text-center text-sm text-muted-foreground"
                data-testid="tile-waiting"
              >
                {path} is not live. It starts playing here when it goes live.
              </div>
            ) : (
              <div className="grid aspect-video place-items-center rounded-lg border border-dashed text-sm text-muted-foreground">
                Choose a stream
              </div>
            )}
          </div>
        ))}
      </div>
    </div>
  )
}
