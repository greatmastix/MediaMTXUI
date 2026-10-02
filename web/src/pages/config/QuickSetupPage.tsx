import { Link } from '@tanstack/react-router'
import {
  ArrowLeft,
  CircleCheck,
  Disc,
  Forward,
  Radio,
  Repeat,
  Upload,
  type LucideIcon,
} from 'lucide-react'
import { useState, type ReactNode } from 'react'

import { ApiError } from '@/api/client'
import { patchGlobal, patchPathDefaults, savePath, type WriteResult } from '@/api/config'
import { fieldClass } from '@/components/config/SettingsForm'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Skeleton } from '@/components/ui/skeleton'
import { globalImpact } from '@/lib/impact'
import {
  enabled,
  protocols,
  publishAddress,
  readAddresses,
  retention,
  type Address,
  type Protocol,
  type Values,
} from '@/lib/quickSetup'
import { cn } from '@/lib/utils'
import { useLive } from '@/live/useLive'

import { useConfig } from './useConfig'

// Quick setup: the things most MediaMTX installations do, each as a few questions, a list of the changes they make,
// and the addresses to use afterwards. Everything is written through the same checked, surgical config writes as the
// full forms, which remain for everything else.

interface Step {
  label: string
  run: () => Promise<WriteResult>
}

interface Done {
  title: string
  path?: string
  publish?: Address
  read?: Address[]
  notes: string[]
}

interface TaskProps {
  host: string
  global: Values
  paths: Values
  pathValues: (name: string) => Values
  onDone: (d: Done) => void
}

interface Task {
  id: string
  title: string
  description: string
  icon: LucideIcon
  Form: (p: TaskProps) => ReactNode
}

const tasks: Task[] = [
  {
    id: 'restream',
    title: 'Re-stream a camera or stream',
    description:
      'Pull from an IP camera or another server and offer it to everyone, over every protocol.',
    icon: Repeat,
    Form: RestreamForm,
  },
  {
    id: 'publish',
    title: 'Receive a stream from OBS or ffmpeg',
    description: 'A publisher pushes to a path; viewers read it over any protocol.',
    icon: Upload,
    Form: PublishForm,
  },
  {
    id: 'forward',
    title: 'Forward a stream to another server',
    description: 'Relay a path to YouTube, Twitch or another MediaMTX, for as long as it is live.',
    icon: Forward,
    Form: ForwardForm,
  },
  {
    id: 'record',
    title: 'Record a stream',
    description:
      'Save a path (or every path) to disk in segments, and delete old ones automatically.',
    icon: Disc,
    Form: RecordForm,
  },
  {
    id: 'protocols',
    title: 'Choose the protocols to serve',
    description:
      'MediaMTX converts between them: whatever comes in can be read over every protocol that is on.',
    icon: Radio,
    Form: ProtocolsForm,
  },
]

export function QuickSetupPage() {
  const { config, global, paths, pathValues, refresh } = useConfig()
  const [task, setTask] = useState<Task | null>(null)
  const [done, setDone] = useState<Done | null>(null)
  if (!config) return <Skeleton className="h-64 w-full" />
  const props: TaskProps = {
    host: config.publicHost,
    global,
    paths,
    pathValues,
    onDone: (d) => {
      setDone(d)
      setTask(null)
      void refresh()
    },
  }
  if (done) {
    return (
      <DonePanel
        done={done}
        onAgain={() => {
          setDone(null)
        }}
      />
    )
  }
  if (!task) {
    return (
      <div className="space-y-3">
        <p className="text-sm text-muted-foreground">
          The usual setups, a few questions each. The other tabs have every setting.
        </p>
        <ul className="grid gap-3 md:grid-cols-2 xl:grid-cols-3" aria-label="Quick setup tasks">
          {tasks.map((t) => (
            <li key={t.id}>
              <button
                type="button"
                onClick={() => {
                  setTask(t)
                }}
                className="flex h-full w-full items-start gap-3 rounded-xl border bg-card p-4 text-left transition-colors hover:border-signal/60 focus-visible:ring-3 focus-visible:ring-ring/50 focus-visible:outline-none"
              >
                <span className="grid size-9 shrink-0 place-items-center rounded-lg bg-signal/15">
                  <t.icon className="size-4.5 text-signal" aria-hidden />
                </span>
                <span className="space-y-1">
                  <span className="block font-medium">{t.title}</span>
                  <span className="block text-sm text-muted-foreground">{t.description}</span>
                </span>
              </button>
            </li>
          ))}
        </ul>
      </div>
    )
  }
  return (
    <div className="max-w-3xl space-y-4">
      <button
        type="button"
        className="inline-flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground"
        onClick={() => {
          setTask(null)
        }}
      >
        <ArrowLeft className="size-4" aria-hidden />
        All tasks
      </button>
      <h2 className="font-heading text-xl font-semibold">{task.title}</h2>
      <task.Form {...props} />
    </div>
  )
}

function DonePanel({ done, onAgain }: { done: Done; onAgain: () => void }) {
  return (
    <div className="max-w-3xl space-y-4 rounded-xl border bg-card p-5" data-testid="quick-done">
      <p className="flex items-center gap-2 font-medium">
        <CircleCheck className="size-5 text-good" aria-hidden />
        {done.title}
      </p>
      {done.publish && <AddressList title="Publish to" addresses={[done.publish]} />}
      {done.read && done.read.length > 0 && (
        <AddressList title="Watch with" addresses={done.read} />
      )}
      {done.notes.map((n) => (
        <p key={n} className="text-sm text-muted-foreground">
          {n}
        </p>
      ))}
      <div className="flex flex-wrap gap-2">
        <Button onClick={onAgain}>Set up something else</Button>
        {done.path && (
          <Link
            to="/config/paths/$"
            params={{ _splat: done.path }}
            className="inline-flex h-8 items-center rounded-lg border px-3 text-sm hover:bg-accent"
          >
            All settings of {done.path}
          </Link>
        )}
      </div>
    </div>
  )
}

function AddressList({ title, addresses }: { title: string; addresses: Address[] }) {
  return (
    <div className="space-y-1.5">
      <p className="section-title">{title}</p>
      <dl className="grid grid-cols-[5rem_minmax(0,1fr)] gap-x-3 gap-y-1 text-sm">
        {addresses.map((a) => (
          <div key={a.protocol} className="contents">
            <dt className="text-muted-foreground">{a.protocol}</dt>
            <dd className="font-mono text-xs break-all select-all">{a.url}</dd>
          </div>
        ))}
      </dl>
    </div>
  )
}

const credentialNote = (actions: string, name: string) =>
  `Clients need a stream credential with the ${actions} action for ${name}: create one on the Credentials page, ` +
  `or turn ${name} into a stream on the Streams page, which gives it its own keys.`

/** The changes a task will make, and the button that makes them. */
function Review({
  changes,
  steps,
  problem,
  warning,
  onDone,
}: {
  changes: string[]
  steps: Step[]
  problem: string | null
  warning?: string | null
  onDone: () => void
}) {
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const run = async () => {
    setError(null)
    setBusy(true)
    try {
      for (const s of steps) await s.run()
      onDone()
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'The change could not be saved.')
    } finally {
      setBusy(false)
    }
  }
  return (
    <div className="space-y-3 rounded-xl border bg-card p-4">
      <p className="section-title">What this changes</p>
      {problem ? (
        <p className="text-sm text-muted-foreground" data-testid="quick-problem">
          {problem}
        </p>
      ) : (
        <ul className="list-disc space-y-1 pl-5 text-sm" aria-label="Changes">
          {changes.map((c) => (
            <li key={c}>{c}</li>
          ))}
        </ul>
      )}
      {warning && !problem && <p className="text-sm text-warning">{warning}</p>}
      {error && (
        <Alert variant="destructive" role="alert">
          <AlertDescription className="whitespace-pre-wrap">{error}</AlertDescription>
        </Alert>
      )}
      <Button
        disabled={busy || !!problem}
        onClick={() => {
          void run()
        }}
      >
        {busy ? 'Saving…' : 'Save'}
      </Button>
    </div>
  )
}

function Field({
  id,
  label,
  hint,
  children,
}: {
  id: string
  label: string
  hint?: string
  children: ReactNode
}) {
  return (
    <div className="space-y-1.5">
      <label htmlFor={id} className="block text-sm font-medium">
        {label}
      </label>
      {children}
      {hint && <p className="text-xs text-muted-foreground">{hint}</p>}
    </div>
  )
}

function Check({
  label,
  hint,
  checked,
  onChange,
}: {
  label: string
  hint?: string
  checked: boolean
  onChange: (v: boolean) => void
}) {
  return (
    <div className="flex items-start gap-2.5">
      <Checkbox
        aria-label={label}
        checked={checked}
        onCheckedChange={(v) => {
          onChange(v)
        }}
        className="mt-0.5"
      />
      <div>
        <p className="text-sm">{label}</p>
        {hint && <p className="text-xs text-muted-foreground">{hint}</p>}
      </div>
    </div>
  )
}

function nameProblem(name: string, paths: Values): string | null {
  const n = name.trim()
  if (!n) return 'Give the path a name.'
  if (!/^[A-Za-z0-9_.~\-/]+$/.test(n) || n.startsWith('/') || n.endsWith('/')) {
    return 'Path names use letters, digits and _ . ~ - / (not at the start or end).'
  }
  if (n in paths) return `There is already a path ${n}; change it under Paths.`
  return null
}

const Frame = ({ children }: { children: ReactNode }) => (
  <div className="space-y-4 rounded-xl border bg-card p-4">{children}</div>
)

function RestreamForm({ host, global, paths, onDone }: TaskProps) {
  const [name, setName] = useState('')
  const [url, setUrl] = useState('')
  const [onDemand, setOnDemand] = useState(true)
  const [record, setRecord] = useState(false)
  const n = name.trim()
  const problem =
    nameProblem(name, paths) ??
    (/^[a-z0-9+.-]+:\/\/./i.test(url.trim())
      ? null
      : 'Enter the address to pull from, e.g. rtsp://user:password@192.168.1.20:554/stream1.')
  const config: Values = {
    source: url.trim(),
    sourceOnDemand: onDemand,
    ...(record ? { record: true } : {}),
  }
  return (
    <>
      <Frame>
        <Field
          id="qs-name"
          label="Path name"
          hint="What readers ask for, e.g. frontdoor or cams/garage."
        >
          <input
            id="qs-name"
            className={cn(fieldClass, 'font-mono')}
            value={name}
            onChange={(e) => {
              setName(e.target.value)
            }}
          />
        </Field>
        <Field
          id="qs-url"
          label="Pull from"
          hint="rtsp://, rtsps://, rtmp://, http(s):// (HLS), srt://, whep(s):// or moqt://. Credentials go in the address."
        >
          <input
            id="qs-url"
            className={cn(fieldClass, 'font-mono')}
            placeholder="rtsp://user:password@192.168.1.20:554/stream1"
            spellCheck={false}
            value={url}
            onChange={(e) => {
              setUrl(e.target.value)
            }}
          />
        </Field>
        <Check
          label="Only connect while someone watches"
          hint="Saves bandwidth and the camera's resources; the first viewer waits a moment."
          checked={onDemand}
          onChange={setOnDemand}
        />
        <Check label="Record it" checked={record} onChange={setRecord} />
      </Frame>
      <Review
        problem={problem}
        changes={[
          `Add path ${n}, pulled from ${url.trim()}${onDemand ? ' while someone watches' : ' all the time'}.`,
          ...(record ? [`Record ${n}.`] : []),
        ]}
        steps={[{ label: 'path', run: () => savePath(n, config, `quick setup: re-stream ${n}`) }]}
        onDone={() => {
          onDone({
            title: `${n} is set up.`,
            path: n,
            read: readAddresses(host, global, n),
            notes: [credentialNote('read', n)],
          })
        }}
      />
    </>
  )
}

const publishProtocols = protocols.filter((p) => p.direct)

function PublishForm({ host, global, paths, onDone }: TaskProps) {
  const [name, setName] = useState('')
  const [proto, setProto] = useState<Protocol['key']>('rtmp')
  const [record, setRecord] = useState(false)
  const n = name.trim()
  const label = protocols.find((p) => p.key === proto)?.label ?? proto
  const off = !enabled(global, proto)
  const after = { ...global, [proto]: true }
  return (
    <>
      <Frame>
        <Field
          id="qs-name"
          label="Path name"
          hint="Where the publisher sends the stream, e.g. live or studio/cam1."
        >
          <input
            id="qs-name"
            className={cn(fieldClass, 'font-mono')}
            value={name}
            onChange={(e) => {
              setName(e.target.value)
            }}
          />
        </Field>
        <Field id="qs-proto" label="The publisher uses">
          <select
            id="qs-proto"
            className={fieldClass}
            value={proto}
            onChange={(e) => {
              setProto(e.target.value as Protocol['key'])
            }}
          >
            {publishProtocols.map((p) => (
              <option key={p.key} value={p.key}>
                {p.label}: {p.about}
              </option>
            ))}
          </select>
        </Field>
        <Check label="Record it" checked={record} onChange={setRecord} />
      </Frame>
      <Review
        problem={nameProblem(name, paths)}
        changes={[
          ...(off ? [`Switch the ${label} server on.`] : []),
          `Add path ${n} for a publisher.`,
          ...(record ? [`Record ${n}.`] : []),
        ]}
        steps={[
          ...(off
            ? [
                {
                  label: 'protocol',
                  run: () =>
                    patchGlobal({
                      set: { [proto]: true },
                      remove: [],
                      reason: `quick setup: ${label} on`,
                    }),
                },
              ]
            : []),
          {
            label: 'path',
            run: () =>
              savePath(
                n,
                { source: 'publisher', ...(record ? { record: true } : {}) },
                `quick setup: publish to ${n}`,
              ),
          },
        ]}
        onDone={() => {
          onDone({
            title: `${n} is ready for a publisher.`,
            path: n,
            publish: { protocol: label, url: publishAddress(host, after, n, proto) },
            read: readAddresses(host, after, n),
            notes: [
              credentialNote('publish', n),
              'In OBS: Settings → Stream → Service "Custom", the RTMP address without the path as Server, the path as Stream Key.',
            ],
          })
        }}
      />
    </>
  )
}

/** Configured paths that name one stream (not all_others or a regular expression). */
const concretePaths = (paths: Values) =>
  Object.keys(paths)
    .filter((k) => k !== 'all_others' && !k.startsWith('~'))
    .sort((a, b) => a.localeCompare(b))

function PathSelect({
  paths,
  value,
  onChange,
  extra,
}: {
  paths: Values
  value: string
  onChange: (v: string) => void
  extra?: ReactNode
}) {
  return (
    <select
      id="qs-path"
      className={fieldClass}
      value={value}
      onChange={(e) => {
        onChange(e.target.value)
      }}
    >
      <option value="">Choose a path…</option>
      {extra}
      {concretePaths(paths).map((p) => (
        <option key={p} value={p}>
          {p}
        </option>
      ))}
    </select>
  )
}

function ForwardForm({ paths, pathValues, onDone }: TaskProps) {
  const [path, setPath] = useState('')
  const [dest, setDest] = useState('')
  const current = path ? pathValues(path) : {}
  const forwards = Array.isArray(current.forward) ? (current.forward as unknown[]) : []
  const problem = !path
    ? 'Choose the path to forward.'
    : /^(rtsps?|rtmps?|srt|moqt|whips?):\/\/./i.test(dest.trim())
      ? null
      : 'Enter where to send it: rtmp(s)://…#streamKey, rtsp(s)://…, srt://…, whip(s)://… or moqt://….'
  return (
    <>
      <Frame>
        <Field id="qs-path" label="Forward">
          <PathSelect paths={paths} value={path} onChange={setPath} />
        </Field>
        <Field
          id="qs-dest"
          label="To"
          hint="YouTube: rtmp://a.rtmp.youtube.com/live2#<stream key>. Twitch: rtmp://live.twitch.tv/app#<stream key>. The stream key is stored in mediamtx.yml."
        >
          <input
            id="qs-dest"
            className={cn(fieldClass, 'font-mono')}
            spellCheck={false}
            placeholder="rtmp://a.rtmp.youtube.com/live2#xxxx-xxxx-xxxx"
            value={dest}
            onChange={(e) => {
              setDest(e.target.value)
            }}
          />
        </Field>
      </Frame>
      <Review
        problem={problem}
        changes={[`Forward ${path} to ${dest.trim().replace(/#.*$/, '#…')} whenever it is live.`]}
        steps={[
          {
            label: 'forward',
            run: () =>
              savePath(
                path,
                { ...current, forward: [...forwards, { dest: dest.trim() }] },
                `quick setup: forward ${path}`,
              ),
          },
        ]}
        onDone={() => {
          onDone({
            title: `${path} is forwarded.`,
            path,
            notes: [
              `MediaMTX pushes ${path} to the destination while it is live, and retries when the connection drops.`,
            ],
          })
        }}
      />
    </>
  )
}

const everyPath = '*every path*'

function RecordForm({ paths, pathValues, onDone }: TaskProps) {
  const [path, setPath] = useState('')
  const [segment, setSegment] = useState('1h')
  const [keep, setKeep] = useState<string>('168h')
  const all = path === everyPath
  const settings: Values = { record: true, recordSegmentDuration: segment, recordDeleteAfter: keep }
  const keepLabel = retention.find((r) => r.value === keep)?.label.toLowerCase() ?? keep
  return (
    <>
      <Frame>
        <Field id="qs-path" label="Record">
          <PathSelect
            paths={paths}
            value={path}
            onChange={setPath}
            extra={<option value={everyPath}>Every path (the path defaults)</option>}
          />
        </Field>
        <Field id="qs-segment" label="Files of">
          <select
            id="qs-segment"
            className={fieldClass}
            value={segment}
            onChange={(e) => {
              setSegment(e.target.value)
            }}
          >
            <option value="10m">10 minutes</option>
            <option value="1h">1 hour</option>
            <option value="6h">6 hours</option>
          </select>
        </Field>
        <Field id="qs-keep" label="Keep">
          <select
            id="qs-keep"
            className={fieldClass}
            value={keep}
            onChange={(e) => {
              setKeep(e.target.value)
            }}
          >
            {retention.map((r) => (
              <option key={r.value} value={r.value}>
                {r.label}
              </option>
            ))}
          </select>
        </Field>
      </Frame>
      <Review
        problem={path ? null : 'Choose what to record.'}
        changes={[
          `Record ${all ? 'every path that does not say otherwise' : path} in files of ${segment}, ${keepLabel}.`,
        ]}
        steps={[
          all
            ? {
                label: 'defaults',
                run: () =>
                  patchPathDefaults({
                    set: settings,
                    remove: [],
                    reason: 'quick setup: record every path',
                  }),
              }
            : {
                label: 'path',
                run: () =>
                  savePath(
                    path,
                    { ...pathValues(path), ...settings },
                    `quick setup: record ${path}`,
                  ),
              },
        ]}
        onDone={() => {
          onDone({
            title: all ? 'Every path is recorded.' : `${path} is recorded.`,
            path: all ? undefined : path,
            notes: [
              'Recordings are written to the recordings volume while the stream is live; find them on the Recordings page.',
            ],
          })
        }}
      />
    </>
  )
}

function ProtocolsForm({ global, onDone }: TaskProps) {
  const live = useLive()
  const [on, setOn] = useState<Record<string, boolean>>(() =>
    Object.fromEntries(protocols.map((p) => [p.key, enabled(global, p.key)])),
  )
  const changed = protocols.filter((p) => on[p.key] !== enabled(global, p.key))
  return (
    <>
      <Frame>
        <p className="text-sm text-muted-foreground">
          MediaMTX remuxes: a stream published over one protocol can be read over every protocol
          that is on, without re-encoding. Only switch on what your clients use.
        </p>
        {protocols.map((p) => (
          <Check
            key={p.key}
            label={p.label}
            hint={p.about + (p.direct ? '' : ' Browsers reach it through this UI.')}
            checked={on[p.key] ?? false}
            onChange={(v) => {
              setOn((o) => ({ ...o, [p.key]: v }))
            }}
          />
        ))}
      </Frame>
      <Review
        problem={changed.length === 0 ? 'Nothing changed yet.' : null}
        changes={changed.map((p) => `Switch the ${p.label} server ${on[p.key] ? 'on' : 'off'}.`)}
        warning={globalImpact(
          changed.map((p) => p.key),
          live.lists,
        )}
        steps={[
          {
            label: 'protocols',
            run: () =>
              patchGlobal({
                set: Object.fromEntries(changed.map((p) => [p.key, on[p.key] ?? false])),
                remove: [],
                reason: 'quick setup: protocols',
              }),
          },
        ]}
        onDone={() => {
          onDone({
            title: 'The protocols are set.',
            notes: [
              'Encoders connect to the stream ports as published (with exposure control on, once the Exposure page opens them).',
            ],
          })
        }}
      />
    </>
  )
}
