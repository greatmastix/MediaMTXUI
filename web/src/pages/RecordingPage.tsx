import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { ChevronLeft, ChevronRight, Download, Play, Trash2 } from 'lucide-react'
import { useRef, useState } from 'react'

import { ApiError } from '@/api/client'
import {
  deleteSegments,
  exportURL,
  recordingsQuery,
  segmentsQuery,
  spansQuery,
  type Span,
} from '@/api/recordings'
import { sessionQuery } from '@/api/sidecar'
import { fieldClass } from '@/components/config/SettingsForm'
import { Button, buttonVariants } from '@/components/ui/button'
import { formatBytes, formatDuration } from '@/lib/format'
import { atLeast } from '@/lib/roles'
import { useNow } from '@/hooks/useNow'

// One path's recordings on a timeline: what was recorded (spans) and the gaps between, segment by segment. Select a
// stretch by dragging (or a minute by clicking, a whole span by double-clicking, or with the fields below), then play
// it here, download it as MP4, or delete its segments.

const zooms = [
  { label: '24 h', ms: 24 * 3600_000 },
  { label: '6 h', ms: 6 * 3600_000 },
  { label: '1 h', ms: 3600_000 },
  { label: '10 min', ms: 600_000 },
]

// Tick spacing for each zoom: a label every so often.
const tickEvery = (span: number) =>
  span > 6 * 3600_000
    ? 3 * 3600_000
    : span > 3600_000
      ? 3600_000
      : span > 600_000
        ? 600_000
        : 60_000

const W = 1000 // the timeline's width in SVG units

const pad = (n: number) => String(n).padStart(2, '0')
const toLocalInput = (d: Date) =>
  `${String(d.getFullYear())}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`
const clock = (d: Date) =>
  d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' })
const day = (d: Date) =>
  d.toLocaleDateString([], { weekday: 'short', year: 'numeric', month: 'short', day: 'numeric' })

interface Selection {
  start: Date
  end: Date
}

export function RecordingPage({ name }: { name: string }) {
  const queryClient = useQueryClient()
  const { data: session } = useQuery(sessionQuery)
  const operator = atLeast(session?.user.role ?? 'viewer', 'operator')
  const info = useQuery(recordingsQuery).data?.paths.find((p) => p.name === name)
  const latest = info ? new Date(info.last) : null
  const now = useNow(60_000)

  // The window shown: by default the 24 h up to an hour after the latest segment.
  const [view, setView] = useState<{ start: number; span: number } | null>(null)
  const span = view?.span ?? zooms[0]?.ms ?? 86_400_000
  const viewStart = view?.start ?? (latest ? latest.getTime() + 3600_000 - span : now - span)
  const viewEnd = viewStart + span
  const startDate = new Date(viewStart)
  const endDate = new Date(viewEnd)

  const spans = useQuery(spansQuery(name, startDate, endDate))
  const segments = useQuery(segmentsQuery(name))
  const [sel, setSel] = useState<Selection | null>(null)
  const [playing, setPlaying] = useState<string | null>(null)
  const [confirm, setConfirm] = useState(false)

  const svg = useRef<SVGSVGElement>(null)
  const drag = useRef<{ from: number; moved: boolean } | null>(null)
  const timeAt = (clientX: number) => {
    const r = svg.current?.getBoundingClientRect()
    if (!r || r.width === 0) return viewStart
    return viewStart + ((clientX - r.left) / r.width) * span
  }
  const x = (t: number) => ((t - viewStart) / span) * W

  const spanList: (Span & { s: number; e: number })[] = (spans.data ?? []).map((sp) => {
    const s = Date.parse(sp.start)
    return { ...sp, s, e: s + sp.duration * 1000 }
  })
  const selSeconds = sel ? (sel.end.getTime() - sel.start.getTime()) / 1000 : 0
  const inSel = sel
    ? (segments.data ?? []).filter((g) => {
        const t = Date.parse(g.start)
        return t >= sel.start.getTime() && t < sel.end.getTime()
      })
    : []

  const remove = useMutation({
    mutationFn: () =>
      deleteSegments(
        name,
        inSel.map((g) => g.start),
      ),
    onSuccess: () => {
      setConfirm(false)
      setPlaying(null)
      void queryClient.invalidateQueries({ queryKey: ['recordings'] })
    },
  })

  const move = (by: number) => {
    setView({ start: viewStart + by, span })
  }
  const zoom = (ms: number) => {
    // Around the selection; else around the latest recording when it is in view; else the middle.
    const last = latest?.getTime()
    const centre = sel
      ? (sel.start.getTime() + sel.end.getTime()) / 2
      : last !== undefined && last >= viewStart && last < viewEnd
        ? last
        : viewStart + span / 2
    setView({ start: centre - ms / 2, span: ms })
  }
  const select = (a: number, b: number) => {
    const [s, e] = a <= b ? [a, b] : [b, a]
    setSel({ start: new Date(s), end: new Date(Math.max(e, s + 1000)) })
    setPlaying(null)
    setConfirm(false)
  }

  const ticks: number[] = []
  const every = tickEvery(span)
  for (let t = Math.ceil(viewStart / every) * every; t < viewEnd; t += every) ticks.push(t)

  return (
    <div className="max-w-5xl space-y-5">
      <div className="space-y-1">
        <Link
          to="/recordings"
          className="inline-flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground"
        >
          <ChevronLeft className="size-4" aria-hidden /> Recordings
        </Link>
        <h1 className="font-heading text-2xl font-semibold break-all">{name}</h1>
        {info && (
          <p className="text-sm text-muted-foreground">
            {formatBytes(info.bytes)} in {info.segments} segments, from {day(new Date(info.first))}{' '}
            to {latest ? `${day(latest)} ${clock(latest)}` : '…'}
          </p>
        )}
      </div>

      <section className="space-y-2" aria-label="Timeline">
        <div className="flex flex-wrap items-center gap-2">
          <Button
            variant="outline"
            size="sm"
            aria-label="Earlier"
            onClick={() => {
              move(-span / 2)
            }}
          >
            <ChevronLeft />
          </Button>
          <span className="min-w-48 text-center text-sm tabular-nums" data-testid="timeline-window">
            {day(startDate)} {clock(startDate)} – {clock(endDate)}
          </span>
          <Button
            variant="outline"
            size="sm"
            aria-label="Later"
            onClick={() => {
              move(span / 2)
            }}
          >
            <ChevronRight />
          </Button>
          <div className="flex gap-1" role="group" aria-label="Zoom">
            {zooms.map((z) => (
              <Button
                key={z.ms}
                size="sm"
                variant={span === z.ms ? 'default' : 'outline'}
                aria-pressed={span === z.ms}
                onClick={() => {
                  zoom(z.ms)
                }}
              >
                {z.label}
              </Button>
            ))}
          </div>
          {latest && (
            <Button
              size="sm"
              variant="ghost"
              onClick={() => {
                setView({ start: latest.getTime() + span * 0.1 - span, span })
              }}
            >
              Latest
            </Button>
          )}
        </div>
        <svg
          ref={svg}
          viewBox={`0 0 ${String(W)} 64`}
          preserveAspectRatio="none"
          className="h-20 w-full cursor-crosshair touch-none rounded-lg border bg-card select-none"
          data-testid="timeline"
          aria-label={`${String(spanList.length)} recorded stretches in this window`}
          role="img"
          onPointerDown={(e) => {
            e.currentTarget.setPointerCapture(e.pointerId)
            drag.current = { from: timeAt(e.clientX), moved: false }
          }}
          onPointerMove={(e) => {
            const d = drag.current
            if (!d) return
            const t = timeAt(e.clientX)
            if (Math.abs(x(t) - x(d.from)) > 3) d.moved = true
            if (d.moved) select(d.from, t)
          }}
          onPointerUp={(e) => {
            const d = drag.current
            drag.current = null
            if (!d) return
            if (d.moved) {
              select(d.from, timeAt(e.clientX))
            } else {
              const sp = spanList.find((s) => d.from >= s.s && d.from < s.e)
              select(d.from, Math.min(d.from + 60_000, sp?.e ?? d.from + 60_000))
            }
          }}
          onDoubleClick={(e) => {
            const t = timeAt(e.clientX)
            const sp = spanList.find((s) => t >= s.s && t < s.e)
            if (sp) select(sp.s, sp.e)
          }}
        >
          {ticks.map((t) => (
            <g key={t}>
              <line x1={x(t)} x2={x(t)} y1={0} y2={44} className="stroke-border" strokeWidth={1} />
              <text x={x(t) + 3} y={58} className="fill-muted-foreground text-[10px]">
                {new Date(t).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}
              </text>
            </g>
          ))}
          {spanList.map((s) => (
            <rect
              key={s.start}
              x={Math.max(0, x(s.s))}
              width={Math.max(1, Math.min(W, x(s.e)) - Math.max(0, x(s.s)))}
              y={10}
              height={30}
              className="fill-signal/70"
              data-testid="timeline-span"
            />
          ))}
          {(segments.data ?? []).map((g) => {
            const t = Date.parse(g.start)
            if (t < viewStart || t >= viewEnd) return null
            return (
              <line
                key={g.start}
                x1={x(t)}
                x2={x(t)}
                y1={6}
                y2={10}
                className="stroke-signal"
                strokeWidth={1}
              />
            )
          })}
          {sel && (
            <rect
              x={Math.max(0, x(sel.start.getTime()))}
              width={Math.max(
                1,
                Math.min(W, x(sel.end.getTime())) - Math.max(0, x(sel.start.getTime())),
              )}
              y={2}
              height={42}
              className="fill-foreground/15 stroke-foreground"
              strokeWidth={1}
              data-testid="timeline-selection"
            />
          )}
        </svg>
        <p className="text-xs text-muted-foreground">
          Drag to select a stretch, click to select a minute, double-click a recorded stretch to
          select all of it. Gaps are times nothing was recorded.
          {spans.data && spanList.length === 0 && ' Nothing was recorded in this window.'}
        </p>
      </section>

      <section className="space-y-3 rounded-xl border bg-card p-4" aria-label="Selection">
        <div className="grid gap-3 sm:grid-cols-[1fr_10rem]">
          <label className="space-y-1 text-sm">
            <span className="font-medium">From</span>
            <input
              type="datetime-local"
              step={1}
              className={fieldClass}
              value={sel ? toLocalInput(sel.start) : ''}
              onChange={(e) => {
                const t = new Date(e.target.value).getTime()
                if (!Number.isNaN(t)) select(t, t + (selSeconds || 60) * 1000)
              }}
            />
          </label>
          <label className="space-y-1 text-sm">
            <span className="font-medium">Length (seconds)</span>
            <input
              type="number"
              min={1}
              className={fieldClass}
              value={sel ? Math.round(selSeconds) : ''}
              onChange={(e) => {
                const n = Number(e.target.value)
                if (sel && n > 0) select(sel.start.getTime(), sel.start.getTime() + n * 1000)
              }}
            />
          </label>
        </div>
        {!sel ? (
          <p className="text-sm text-muted-foreground">Select a stretch on the timeline.</p>
        ) : (
          <>
            <p className="text-sm" data-testid="selection-summary">
              {day(sel.start)} {clock(sel.start)} – {clock(sel.end)} ({formatDuration(selSeconds)})
              {inSel.length > 0 &&
                ` · ${String(inSel.length)} segment${inSel.length === 1 ? '' : 's'} start in it`}
            </p>
            <div className="flex flex-wrap gap-2">
              <Button
                size="sm"
                onClick={() => {
                  setPlaying(exportURL(name, sel.start, selSeconds, 'fmp4'))
                }}
              >
                <Play /> Play
              </Button>
              <a
                className={buttonVariants({ size: 'sm', variant: 'outline' })}
                href={exportURL(name, sel.start, selSeconds, 'mp4')}
                download
              >
                <Download /> Download MP4
              </a>
              {operator && inSel.length > 0 && !confirm && (
                <Button
                  size="sm"
                  variant="outline"
                  onClick={() => {
                    setConfirm(true)
                  }}
                >
                  <Trash2 /> Delete {inSel.length} segment{inSel.length === 1 ? '' : 's'}
                </Button>
              )}
            </div>
            {confirm && (
              <div
                role="alertdialog"
                aria-label="Delete recordings"
                className="flex flex-wrap items-center gap-2 rounded-lg border border-destructive/40 p-3 text-sm"
              >
                <span>
                  Delete the {inSel.length} segment{inSel.length === 1 ? '' : 's'} that start in
                  this stretch? A segment is deleted whole, so a little past the end may go too.
                  This cannot be undone.
                </span>
                <Button
                  size="sm"
                  variant="destructive"
                  disabled={remove.isPending}
                  onClick={() => {
                    remove.mutate()
                  }}
                >
                  Delete
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
              </div>
            )}
            {remove.data && (
              <p role="status" className="text-sm">
                Deleted {remove.data.deleted} segment{remove.data.deleted === 1 ? '' : 's'}
                {remove.data.failed > 0 && `; ${String(remove.data.failed)} could not be deleted`}.
              </p>
            )}
            {remove.error && (
              <p role="alert" className="text-sm text-destructive">
                {remove.error instanceof ApiError
                  ? remove.error.message
                  : 'The segments were not deleted.'}
              </p>
            )}
            {playing && (
              <video
                key={playing}
                src={playing}
                controls
                autoPlay
                muted
                className="aspect-video w-full max-w-2xl rounded-lg bg-black"
                data-testid="recording-player"
              />
            )}
          </>
        )}
      </section>
    </div>
  )
}
