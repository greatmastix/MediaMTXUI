import { useQuery } from '@tanstack/react-query'
import { Download, FileText, Pause, Play } from 'lucide-react'
import { useEffect, useLayoutEffect, useRef, useState } from 'react'

import {
  logDownloadURL,
  logLineSchema,
  logLinesSchema,
  logSearchQuery,
  logStreamURL,
  type LogFilter,
  type LogLine,
} from '@/api/logs'
import { sessionQuery } from '@/api/sidecar'
import { fieldClass } from '@/components/config/SettingsForm'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button, buttonVariants } from '@/components/ui/button'
import { atLeast } from '@/lib/roles'
import { cn } from '@/lib/utils'

// Logs (admins): MediaMTX's log and the sidecar's own, tailed live (the newest lines, then new ones as they are
// written) or searched (up to 2000 matches, rotated copies included).

const keepLines = 2000

const levelClass: Record<LogLine['level'], string> = {
  debug: 'text-muted-foreground',
  info: 'text-foreground',
  warn: 'text-warning',
  error: 'text-destructive',
}

type TailState = 'connecting' | 'live' | 'reconnecting'

/** A line with a number that stays the same while the view scrolls on (its key). */
type Numbered = LogLine & { n: number }

const numbered = (lines: LogLine[], from = 0): Numbered[] =>
  lines.map((l, i) => ({ ...l, n: from + i }))

/** A live tail over SSE; it reconnects by itself (the browser's EventSource) and starts again from the backlog. */
function useLogTail(filter: LogFilter, enabled: boolean) {
  const [lines, setLines] = useState<Numbered[]>([])
  const [state, setState] = useState<TailState>('connecting')
  const url = logStreamURL(filter)
  useEffect(() => {
    if (!enabled) return
    const es = new EventSource(url)
    es.addEventListener('backlog', (e) => {
      setLines(numbered(logLinesSchema.parse(JSON.parse((e as MessageEvent<string>).data)).lines))
      setState('live')
    })
    es.addEventListener('line', (e) => {
      const l = logLineSchema.parse(JSON.parse((e as MessageEvent<string>).data))
      setLines((prev) => [...prev.slice(-(keepLines - 1)), { ...l, n: (prev.at(-1)?.n ?? 0) + 1 }])
    })
    es.addEventListener('session', () => {
      es.close()
    })
    es.onerror = () => {
      setState('reconnecting')
    }
    return () => {
      es.close()
      setLines([])
      setState('connecting')
    }
  }, [url, enabled])
  return { lines, state }
}

export function LogsPage() {
  const { data: session } = useQuery(sessionQuery)
  const admin = atLeast(session?.user.role ?? 'viewer', 'admin')
  const [source, setSource] = useState<LogFilter['source']>('mediamtx')
  const [level, setLevel] = useState<LogFilter['level']>('')
  const [draft, setDraft] = useState('')
  const [q, setQ] = useState('')
  const [live, setLive] = useState(true)
  const filter: LogFilter = { source, level, q }
  const tail = useLogTail(filter, admin && live)
  const search = useQuery({ ...logSearchQuery(filter), enabled: admin && !live })
  const lines = live ? tail.lines : numbered(search.data?.lines ?? [])

  if (!admin) {
    return (
      <div className="space-y-2">
        <h1 className="font-heading text-2xl font-semibold">Logs</h1>
        <p className="text-sm text-muted-foreground">The logs are for admins.</p>
      </div>
    )
  }
  return (
    <div className="space-y-4">
      <div className="space-y-1">
        <h1 className="flex items-center gap-2 font-heading text-2xl font-semibold">
          <FileText className="size-6 text-signal" aria-hidden /> Logs
        </h1>
        <p className="text-sm text-muted-foreground">
          MediaMTX&apos;s log (kept in rotated copies as it grows) and the sidecar&apos;s own recent
          lines. Times are yours.
        </p>
      </div>
      <form
        className="flex flex-wrap items-end gap-3 rounded-xl border bg-card p-4"
        aria-label="Filter"
        onSubmit={(e) => {
          e.preventDefault()
          setQ(draft.trim())
        }}
      >
        <label className="space-y-1 text-sm">
          <span className="font-medium">Log</span>
          <select
            className={fieldClass}
            value={source}
            onChange={(e) => {
              setSource(e.target.value as LogFilter['source'])
            }}
          >
            <option value="mediamtx">MediaMTX</option>
            <option value="sidecar">Sidecar</option>
          </select>
        </label>
        <label className="space-y-1 text-sm">
          <span className="font-medium">Level</span>
          <select
            className={fieldClass}
            value={level}
            onChange={(e) => {
              setLevel(e.target.value as LogFilter['level'])
            }}
          >
            <option value="">Everything</option>
            <option value="info">Info and above</option>
            <option value="warn">Warnings and errors</option>
            <option value="error">Errors</option>
          </select>
        </label>
        <label className="min-w-48 flex-1 space-y-1 text-sm">
          <span className="font-medium">Containing</span>
          <input
            className={fieldClass}
            value={draft}
            maxLength={200}
            placeholder="a path, an address, a word…"
            onChange={(e) => {
              setDraft(e.target.value)
            }}
          />
        </label>
        <Button type="submit">Show</Button>
        <Button
          type="button"
          variant="outline"
          aria-pressed={live}
          onClick={() => {
            setLive(!live)
          }}
        >
          {live ? <Pause /> : <Play />} {live ? 'Stop following' : 'Follow live'}
        </Button>
        {source === 'mediamtx' && (
          <a className={buttonVariants({ variant: 'outline' })} href={logDownloadURL} download>
            <Download /> Download
          </a>
        )}
      </form>
      <p className="text-xs text-muted-foreground" role="status" data-testid="log-state">
        {live
          ? tail.state === 'live'
            ? `Following live: the newest ${String(lines.length)} lines, new ones as they come.`
            : tail.state === 'reconnecting'
              ? 'Reconnecting…'
              : 'Connecting…'
          : search.isFetching
            ? 'Searching…'
            : `${String(lines.length)} lines${search.data?.more ? ', the newest of more matches: narrow the search to see older ones' : ''}.`}
      </p>
      {search.error && !live && (
        <Alert variant="destructive" role="alert">
          <AlertDescription>{search.error.message}</AlertDescription>
        </Alert>
      )}
      <LogView lines={lines} follow={live} />
    </div>
  )
}

/** The lines, newest at the bottom; while following, it stays scrolled to the bottom unless you scrolled up. */
function LogView({ lines, follow }: { lines: Numbered[]; follow: boolean }) {
  const box = useRef<HTMLDivElement>(null)
  const atBottom = useRef(true)
  useLayoutEffect(() => {
    const el = box.current
    if (el && (atBottom.current || !follow)) el.scrollTop = el.scrollHeight
  }, [lines, follow])
  if (lines.length === 0) {
    return (
      <p className="rounded-lg border border-dashed p-8 text-center text-sm text-muted-foreground">
        No lines.
      </p>
    )
  }
  return (
    <div
      ref={box}
      className="max-h-[65vh] overflow-auto rounded-lg border bg-card p-2 font-mono text-xs"
      onScroll={(e) => {
        const el = e.currentTarget
        atBottom.current = el.scrollHeight - el.scrollTop - el.clientHeight < 24
      }}
    >
      <ol aria-label="Log lines" className="space-y-0.5">
        {lines.map((l) => (
          <li
            key={l.n}
            data-testid="log-line"
            data-level={l.level}
            className={cn('flex gap-3 break-all whitespace-pre-wrap', levelClass[l.level])}
          >
            <span className="shrink-0 text-muted-foreground tabular-nums">
              {l.t ? new Date(l.t).toLocaleString('en-GB') : ''}
            </span>
            <span className="w-10 shrink-0 uppercase">{l.level}</span>
            <span className="min-w-0">{l.text}</span>
          </li>
        ))}
      </ol>
    </div>
  )
}
