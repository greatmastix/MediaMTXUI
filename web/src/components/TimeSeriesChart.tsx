import { useId, useState, type PointerEvent } from 'react'

import { formatClock, formatDay, formatDayClock, niceCeiling } from '@/lib/format'
import { cn } from '@/lib/utils'

// A small line chart for the dashboard, drawn as SVG without a charting library. Time runs along a window ending now:
// for the live hour, the history's span rounded up to 5 minutes, so a fresh start is not a sliver at the right edge;
// for the longer ranges, the whole range. A gap in the samples (MediaMTX unreachable, the sidecar down) breaks the line.

export interface Series {
  label: string
  /** A text colour class; the line and its area use currentColor. */
  className: string
  value: (i: number) => number | null
}

interface Props {
  title: string
  times: readonly number[]
  series: readonly Series[]
  format: (v: number) => string
  now: number
  /** The longest window; the default is the history the sidecar keeps. */
  maxWindowMs?: number
  /** A fixed window (the longer ranges), instead of one that grows with the history. */
  windowMs?: number
  /** Samples further apart than this are not joined. */
  gapMs?: number
}

const W = 600
const H = 160

export function TimeSeriesChart({
  title,
  times,
  series,
  format,
  now,
  maxWindowMs = 3_600_000,
  windowMs: fixedWindow,
  gapMs = 12_000,
}: Props) {
  const [hover, setHover] = useState<number | null>(null)
  const titleId = useId()
  const step = 300_000
  const span = times.length > 0 ? now - (times[0] ?? now) : 0
  const windowMs =
    fixedWindow ?? Math.min(maxWindowMs, Math.max(step, Math.ceil(span / step) * step))
  const minutes = Math.round(windowMs / 60_000)
  const days = windowMs > 86_400_000
  const when = days ? formatDayClock : formatClock
  const lastWhat =
    minutes <= 60
      ? minutes === 60
        ? 'hour'
        : `${String(minutes)} minutes`
      : minutes < 2880
        ? `${String(Math.round(minutes / 60))} hours`
        : `${String(Math.round(minutes / 1440))} days`
  const start = now - windowMs
  const first = times.findIndex((t) => t >= start)
  const idx = first < 0 ? [] : times.map((_, i) => i).slice(first)

  let max = 0
  for (const s of series) {
    for (const i of idx) max = Math.max(max, s.value(i) ?? 0)
  }
  const top = niceCeiling(max)
  const x = (t: number) => ((t - start) / windowMs) * W
  const y = (v: number) => H - (v / top) * (H - 4) - 2

  const paths = series.map((s) => {
    let d = ''
    let prevT: number | null = null
    for (const i of idx) {
      const v = s.value(i)
      const t = times[i] ?? 0
      if (v == null) {
        prevT = null
        continue
      }
      const joined = prevT != null && t - prevT <= gapMs
      d += `${joined ? 'L' : 'M'}${x(t).toFixed(1)},${y(v).toFixed(1)}`
      prevT = t
    }
    return d
  })

  const latest = idx.at(-1)
  const shown = hover ?? latest
  const onMove = (e: PointerEvent<SVGSVGElement>) => {
    const box = e.currentTarget.getBoundingClientRect()
    const t = start + ((e.clientX - box.left) / box.width) * windowMs
    let best: number | null = null
    for (const i of idx) {
      if (best == null || Math.abs((times[i] ?? 0) - t) < Math.abs((times[best] ?? 0) - t)) best = i
    }
    setHover(best)
  }

  const summary =
    latest == null
      ? 'no data yet'
      : series.map((s) => `${s.label} ${fmt(s.value(latest), format)}`).join(', ')

  return (
    <figure className="space-y-2" aria-labelledby={titleId}>
      <figcaption className="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1">
        <span id={titleId} className="text-sm font-medium">
          {title}
        </span>
        <span className="flex flex-wrap gap-x-3 text-xs text-muted-foreground tabular-nums">
          {shown != null && hover != null && <span>{when(times[shown] ?? 0)}</span>}
          {series.map((s) => (
            <span key={s.label} className="flex items-center gap-1">
              <span
                className={cn('inline-block h-[3px] w-2.5 rounded-sm bg-current', s.className)}
              />
              {s.label} {shown == null ? '–' : fmt(s.value(shown), format)}
            </span>
          ))}
        </span>
      </figcaption>
      <div className="relative">
        <svg
          viewBox={`0 0 ${W} ${H}`}
          preserveAspectRatio="none"
          className="h-40 w-full touch-none overflow-visible"
          role="img"
          aria-label={`${title}, last ${lastWhat}: ${summary}`}
          onPointerMove={onMove}
          onPointerLeave={() => {
            setHover(null)
          }}
        >
          {[0.25, 0.5, 0.75].map((f) => (
            <line
              key={f}
              x1={0}
              x2={W}
              y1={y(top * f)}
              y2={y(top * f)}
              className="stroke-border"
              strokeDasharray="4 4"
              vectorEffect="non-scaling-stroke"
            />
          ))}
          <line
            x1={0}
            x2={W}
            y1={H - 1}
            y2={H - 1}
            className="stroke-border"
            vectorEffect="non-scaling-stroke"
          />
          {paths.map((d, i) => (
            <path
              key={series[i]?.label}
              d={d}
              fill="none"
              stroke="currentColor"
              strokeWidth={2}
              strokeLinejoin="round"
              vectorEffect="non-scaling-stroke"
              className={series[i]?.className}
            />
          ))}
          {hover != null && (
            <line
              x1={x(times[hover] ?? 0)}
              x2={x(times[hover] ?? 0)}
              y1={0}
              y2={H}
              className="stroke-muted-foreground"
              vectorEffect="non-scaling-stroke"
            />
          )}
        </svg>
        <span className="pointer-events-none absolute top-0 left-1 text-[10px] text-muted-foreground tabular-nums">
          {format(top)}
        </span>
      </div>
      <div
        className="flex justify-between text-[10px] text-muted-foreground tabular-nums"
        aria-hidden
      >
        <span>{days ? formatDay(start) : formatClock(start)}</span>
        <span>{days ? formatDay(start + windowMs / 2) : formatClock(start + windowMs / 2)}</span>
        <span>now</span>
      </div>
    </figure>
  )
}

function fmt(v: number | null, format: (v: number) => string) {
  return v == null ? '–' : format(v)
}
