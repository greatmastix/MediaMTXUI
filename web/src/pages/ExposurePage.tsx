import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { ShieldOff } from 'lucide-react'
import { useState } from 'react'

import { ApiError } from '@/api/client'
import { configQuery } from '@/api/config'
import {
  closeAll,
  closePort,
  durationHours,
  exposureQuery,
  exposureSchema,
  openPort,
  setAutoRules,
  type AutoRules,
  type Exposure,
  type OpenRequest,
  type PortStatus,
} from '@/api/exposure'
import { sessionQuery } from '@/api/sidecar'
import { fieldClass } from '@/components/config/SettingsForm'
import { StatusPill } from '@/components/StatusPill'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { useNow } from '@/hooks/useNow'
import { formatDuration, formatTime } from '@/lib/format'
import { enabled, portOf, protocols, type Values } from '@/lib/quickSetup'
import { atLeast } from '@/lib/roles'
import { useLive } from '@/live/useLive'

// Exposure control: which stream ports the internet can reach, from where and until when. The page shows what the
// host helper reports it did (streamed live), and marks a change as applying until the helper has caught up.

const names: Record<string, { label: string; key: 'rtsp' | 'rtmp' | 'srt' | 'webrtc' }> = {
  rtsp: { label: 'RTSP', key: 'rtsp' },
  rtmp: { label: 'RTMP', key: 'rtmp' },
  srt: { label: 'SRT', key: 'srt' },
  webrtc: { label: 'WebRTC media', key: 'webrtc' },
}

const durations = [1, 4, 12, 24, 24 * 7, 24 * 30]

function hoursLabel(h: number) {
  if (h < 24) return `${String(h)} hour${h === 1 ? '' : 's'}`
  const d = h / 24
  return `${String(d)} day${d === 1 ? '' : 's'}`
}

function errorText(e: unknown) {
  return e instanceof ApiError ? e.message : 'The request failed.'
}

export function ExposurePage() {
  const { data: session } = useQuery(sessionQuery)
  const admin = atLeast(session?.user.role ?? 'viewer', 'admin')
  const initial = useQuery({ ...exposureQuery, enabled: admin })
  const live = exposureSchema.safeParse(useLive().extras.exposure)
  const view: Exposure | undefined = live.success ? live.data : initial.data
  const { data: config } = useQuery({ ...configQuery, enabled: admin })
  const global = (config?.settings ?? {}) as Values
  const queryClient = useQueryClient()
  const [confirmAll, setConfirmAll] = useState(false)
  const all = useMutation({
    mutationFn: closeAll,
    onSuccess: () => {
      setConfirmAll(false)
      void queryClient.invalidateQueries({ queryKey: exposureQuery.queryKey })
    },
  })

  if (!admin) {
    return (
      <div className="space-y-2">
        <h1 className="font-heading text-2xl font-semibold">Exposure</h1>
        <p className="text-sm text-muted-foreground">Exposure control is for admins.</p>
      </div>
    )
  }
  const status = view?.status
  const anyOpen = Object.values(status?.ports ?? {}).some((p) => p.state !== 'closed')
  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-end justify-between gap-3">
        <div className="max-w-3xl space-y-1">
          <h1 className="font-heading text-2xl font-semibold">Exposure</h1>
          <p className="text-sm text-muted-foreground">
            Which stream ports the internet can reach. A protocol works from outside only when it is
            switched on in <Link to="/config">Configuration</Link> and its port is open here. The
            host&apos;s firewall helper applies every change within its policy and reports back:
            this page shows what it did, not what was asked.
          </p>
        </div>
        {status &&
          (confirmAll ? (
            <div className="flex items-center gap-2">
              <span className="text-sm">
                Close every port now? This also switches the automatic rules off.
              </span>
              <Button
                variant="destructive"
                onClick={() => {
                  all.mutate()
                }}
                disabled={all.isPending}
              >
                Close all
              </Button>
              <Button
                variant="outline"
                onClick={() => {
                  setConfirmAll(false)
                }}
              >
                Cancel
              </Button>
            </div>
          ) : (
            <Button
              variant="outline"
              onClick={() => {
                setConfirmAll(true)
              }}
              disabled={!anyOpen}
            >
              <ShieldOff /> Close all exposure
            </Button>
          ))}
      </div>
      {all.error && (
        <Alert variant="destructive" role="alert">
          <AlertDescription>{errorText(all.error)}</AlertDescription>
        </Alert>
      )}
      {!view ? (
        <Skeleton className="h-40 w-full max-w-4xl" />
      ) : !view.installed || !status ? (
        <NotInstalled global={global} />
      ) : (
        <>
          {status.error && (
            <Alert variant="destructive" role="alert" data-testid="exposure-error">
              <AlertDescription>The firewall helper reports: {status.error}</AlertDescription>
            </Alert>
          )}
          {status.closedAll && (
            <p className="text-sm text-muted-foreground" data-testid="exposure-closed-all">
              Everything was closed on the host at {formatTime(status.closedAll)}.
            </p>
          )}
          <AutomaticRules view={view} />
          <ul className="max-w-4xl space-y-2" aria-label="Stream ports">
            {Object.entries(status.ports)
              .sort(([a], [b]) => a.localeCompare(b))
              .map(([id, p]) => (
                <PortRow
                  key={id}
                  id={id}
                  port={p}
                  view={view}
                  listening={names[id] && config ? enabled(global, names[id].key) : undefined}
                  clientIP={initial.data?.clientIP}
                />
              ))}
          </ul>
          <p className="text-xs text-muted-foreground">
            Helper: {status.driver} driver, last run {formatTime(status.updated)}, request{' '}
            {String(status.rev)}. Limits: anyone for at most{' '}
            {hoursLabel(durationHours(status.limits.maxTTLAnySource))}
            {status.limits.allowAnySource ? '' : ' (not allowed)'}, named addresses for at most{' '}
            {status.limits.permanentOK ? 'ever' : hoursLabel(durationHours(status.limits.maxTTL))}.
          </p>
        </>
      )}
    </div>
  )
}

function applying(view: Exposure, id: string, port: PortStatus): boolean {
  if (!view.pending || !view.desired) return false
  const want = view.desired.want[id]
  return want ? port.state !== 'open' : port.state === 'open'
}

function PortRow({
  id,
  port,
  view,
  listening,
  clientIP,
}: {
  id: string
  port: PortStatus
  view: Exposure
  listening: boolean | undefined
  clientIP: string | undefined
}) {
  const now = useNow()
  const [editing, setEditing] = useState(false)
  const queryClient = useQueryClient()
  const close = useMutation({
    mutationFn: () => closePort(id),
    onSuccess: () => void queryClient.invalidateQueries({ queryKey: exposureQuery.queryKey }),
  })
  const label = names[id]?.label ?? id
  const until = port.until ? Date.parse(port.until) : null
  const busy = applying(view, id, port)
  const manual = view.manual?.want[id] !== undefined
  const automatic = (view.auto ?? []).filter((a) => a.port === id)
  return (
    <li className="rounded-xl border bg-card p-3" data-testid={`exposure-${id}`}>
      <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
        <div className="w-32">
          <p className="font-medium">{label}</p>
          <p className="font-mono text-xs text-muted-foreground">
            {String(port.port)}/{port.proto}
          </p>
        </div>
        <div className="flex flex-1 flex-wrap items-center gap-2 text-sm">
          {busy ? (
            <StatusPill tone="neutral">Applying…</StatusPill>
          ) : port.state === 'open' ? (
            <StatusPill tone="good">Open</StatusPill>
          ) : port.state === 'error' ? (
            <StatusPill tone="warning">Error</StatusPill>
          ) : (
            <StatusPill tone="neutral">Closed</StatusPill>
          )}
          {port.state !== 'closed' && (
            <span>
              to{' '}
              {port.sources?.length ? (
                <span className="font-mono text-xs">{port.sources.join(', ')}</span>
              ) : (
                'anyone'
              )}
              {until !== null
                ? `, ${until > now ? `closes in ${formatDuration((until - now) / 1000)}` : 'closing'}`
                : ', no expiry'}
            </span>
          )}
          {listening === false && (
            <span className="text-xs text-muted-foreground">
              {label} is switched off in MediaMTX: nothing answers on this port.
            </span>
          )}
        </div>
        <div className="flex gap-2">
          <Button
            variant="outline"
            size="sm"
            onClick={() => {
              setEditing(!editing)
            }}
            aria-expanded={editing}
          >
            {port.state === 'closed' ? 'Open…' : 'Change…'}
          </Button>
          {manual && (
            <Button
              variant="outline"
              size="sm"
              onClick={() => {
                close.mutate()
              }}
              disabled={close.isPending}
            >
              Close
            </Button>
          )}
        </div>
      </div>
      {automatic.length > 0 && (
        <p className="mt-2 text-xs text-muted-foreground">
          Opened automatically: {[...new Set(automatic.map((a) => a.reason))].join('; ')}
          {manual ? ', and by hand.' : '.'}
        </p>
      )}
      {port.error && <p className="mt-2 text-xs text-destructive">{port.error}</p>}
      {close.error && <p className="mt-2 text-xs text-destructive">{errorText(close.error)}</p>}
      {editing && view.status && (
        <OpenForm
          id={id}
          label={label}
          limits={view.status.limits}
          clientIP={clientIP}
          onDone={() => {
            setEditing(false)
          }}
        />
      )}
    </li>
  )
}

function OpenForm({
  id,
  label,
  limits,
  clientIP,
  onDone,
}: {
  id: string
  label: string
  limits: NonNullable<Exposure['status']>['limits']
  clientIP: string | undefined
  onDone: () => void
}) {
  const [who, setWho] = useState<'me' | 'these' | 'anyone'>('me')
  const [sources, setSources] = useState('')
  const maxHours = durationHours(who === 'anyone' ? limits.maxTTLAnySource : limits.maxTTL)
  const choices = durations.filter((h) => h <= maxHours)
  const [hours, setHours] = useState<number | 'permanent'>(1)
  const permanentOK = limits.permanentOK && who !== 'anyone'
  const effective = hours === 'permanent' ? (permanentOK ? hours : 1) : Math.min(hours, maxHours)
  const queryClient = useQueryClient()
  const open = useMutation({
    mutationFn: (r: OpenRequest) => openPort(id, r),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: exposureQuery.queryKey })
      onDone()
    },
  })
  const radio = (value: typeof who, text: string, disabled = false) => (
    <label className="flex items-center gap-2 text-sm">
      <input
        type="radio"
        name={`who-${id}`}
        value={value}
        checked={who === value}
        disabled={disabled}
        onChange={() => {
          setWho(value)
        }}
      />
      {text}
    </label>
  )
  return (
    <form
      className="mt-3 space-y-3 border-t pt-3"
      aria-label={`Open ${label}`}
      onSubmit={(e) => {
        e.preventDefault()
        open.mutate({
          sources:
            who === 'these'
              ? sources
                  .split(',')
                  .map((s) => s.trim())
                  .filter(Boolean)
              : [],
          myAddress: who === 'me',
          hours: effective === 'permanent' ? 0 : effective,
          permanent: effective === 'permanent',
        })
      }}
    >
      <fieldset className="space-y-1.5">
        <legend className="section-title mb-1">Reachable from</legend>
        {radio('me', `My address${clientIP ? ` (${clientIP})` : ''}`)}
        {radio('these', 'These addresses or ranges')}
        {who === 'these' && (
          <input
            className={fieldClass}
            aria-label="Addresses"
            placeholder="203.0.113.7, 198.51.100.0/24"
            value={sources}
            onChange={(e) => {
              setSources(e.target.value)
            }}
          />
        )}
        {radio(
          'anyone',
          limits.allowAnySource ? 'Anyone' : 'Anyone (the policy does not allow it)',
          !limits.allowAnySource,
        )}
      </fieldset>
      <label className="flex items-center gap-2 text-sm">
        For
        <select
          className={fieldClass + ' w-auto'}
          value={String(effective)}
          onChange={(e) => {
            setHours(e.target.value === 'permanent' ? 'permanent' : Number(e.target.value))
          }}
        >
          {choices.map((h) => (
            <option key={h} value={h}>
              {hoursLabel(h)}
            </option>
          ))}
          {permanentOK && <option value="permanent">No expiry</option>}
        </select>
      </label>
      {open.error && (
        <p className="text-sm text-destructive" role="alert">
          {errorText(open.error)}
        </p>
      )}
      <div className="flex gap-2">
        <Button type="submit" disabled={open.isPending || (who === 'these' && !sources.trim())}>
          Open {label}
        </Button>
        <Button type="button" variant="outline" onClick={onDone}>
          Cancel
        </Button>
      </div>
    </form>
  )
}

/** Without the helper: what to run by hand, for the protocols that are switched on. */
function NotInstalled({ global }: { global: Values }) {
  const on = protocols.filter((p) => p.direct && enabled(global, p.key))
  return (
    <div
      className="max-w-3xl space-y-3 rounded-xl border bg-card p-4 text-sm"
      data-testid="exposure-not-installed"
    >
      <p>
        Exposure control is not installed on this host, so this page cannot open or close ports. The
        stream ports stay as your firewall has them. To let a client in by hand:
      </p>
      <pre className="overflow-x-auto rounded-lg bg-background p-3 font-mono text-xs">
        {on
          .map((p) => {
            const proto = p.key === 'srt' ? 'udp' : 'tcp'
            return `sudo ufw allow proto ${proto} from <client address> to any port ${String(portOf(global, p))}  # ${p.label}`
          })
          .concat([
            'sudo ufw allow proto udp from <client address> to any port 8189  # WebRTC media',
          ])
          .join('\n')}
      </pre>
      <p className="text-muted-foreground">
        The helper (mtx-portgate) and what it needs are described in the project&apos;s
        docs/exposure-control.md; deploy/portgate/install.sh installs it.
      </p>
    </div>
  )
}

const ruleText: { key: keyof AutoRules; label: string; hint: string }[] = [
  {
    key: 'publish',
    label: 'Let encoders in while a stream is set up or live',
    hint: "While a stream page is open, its publishing ports accept that browser's address; while a stream is live, the port it uses stays open to its encoder.",
  },
  {
    key: 'remember',
    label: 'Remember encoders for 30 days',
    hint: 'An address that streamed successfully keeps getting in for 30 days after its last stream (three per stream), so an encoder can start without anyone opening a page.',
  },
  {
    key: 'viewers',
    label: 'Let players and viewers in while something is live',
    hint: 'RTSP, RTMP, SRT and WebRTC media are open to anyone while a path is live, so players such as VRChat or VLC and outside browsers reach them. Playback keys still decide who may watch.',
  },
]

const sameRules = (a: AutoRules | undefined, b: AutoRules | undefined) =>
  a?.publish === b?.publish && a?.remember === b?.remember && a?.viewers === b?.viewers

/** The automatic rules, each switchable, and what they have opened right now. */
function AutomaticRules({ view }: { view: Exposure }) {
  const queryClient = useQueryClient()
  // The choices made here that the server has not reported back yet, and its rules before them: the last choice wins
  // over the streamed state meanwhile, so quick clicks do not undo each other. Once the server reports it, or reports
  // rules that are none of these while nothing is being saved (another admin, or Close all, which switches them all
  // off), the server's rules show again: the next click starts from what is in force, never from an older choice.
  const [local, setLocal] = useState<{ sent: AutoRules[]; over: AutoRules | undefined } | null>(
    null,
  )
  const save = useMutation({
    mutationFn: setAutoRules,
    onSuccess: () => void queryClient.invalidateQueries({ queryKey: exposureQuery.queryKey }),
    onError: () => {
      setLocal(null)
    },
  })
  const chosen = local?.sent.at(-1)
  if (
    local &&
    (sameRules(chosen, view.rules) ||
      (!save.isPending && ![local.over, ...local.sent].some((r) => sameRules(r, view.rules))))
  ) {
    setLocal(null)
  }
  const rules = chosen ?? view.rules ?? { publish: true, remember: true, viewers: true }
  const now = useNow()
  const auto = view.auto ?? []
  return (
    <section
      className="max-w-4xl space-y-3 rounded-xl border bg-card p-4"
      aria-labelledby="auto-title"
    >
      <h2 id="auto-title" className="font-medium">
        Automatic
      </h2>
      <p className="text-sm text-muted-foreground">
        Ports open by themselves while they are in use and close again afterwards. Stream keys still
        decide who may publish or watch; these rules only decide who can reach the ports.
      </p>
      <ul className="space-y-2">
        {ruleText.map((r) => (
          <li key={r.key}>
            <label className="flex items-start gap-2 text-sm">
              <input
                type="checkbox"
                className="mt-1"
                checked={rules[r.key]}
                onChange={(e) => {
                  const next = { ...rules, [r.key]: e.target.checked }
                  setLocal({
                    sent: [...(local?.sent ?? []), next],
                    over: local ? local.over : view.rules,
                  })
                  save.mutate(next)
                }}
              />
              <span>
                <span className="font-medium">{r.label}</span>
                <span className="block text-xs text-muted-foreground">{r.hint}</span>
              </span>
            </label>
          </li>
        ))}
      </ul>
      {auto.length > 0 && (
        <ul
          className="space-y-1 text-xs"
          aria-label="Automatic openings"
          data-testid="auto-openings"
        >
          {auto.map((a) => (
            <li key={`${a.port} ${a.source} ${a.reason}`} className="flex flex-wrap gap-x-2">
              <span className="font-medium">{names[a.port]?.label ?? a.port}</span>
              <span className="font-mono">{a.source || 'anyone'}</span>
              <span className="text-muted-foreground">
                {a.reason}, until{' '}
                {Date.parse(a.until) - now > 86_400_000
                  ? formatTime(a.until)
                  : `${formatDuration(Math.max(0, (Date.parse(a.until) - now) / 1000))} from now`}
              </span>
            </li>
          ))}
        </ul>
      )}
      {save.error && (
        <p role="alert" className="text-sm text-destructive">
          {errorText(save.error)}
        </p>
      )}
    </section>
  )
}
