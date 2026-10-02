import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { KeyRound, Plus } from 'lucide-react'
import { useState, type ReactNode } from 'react'

import { ApiError } from '@/api/client'
import { configQuery } from '@/api/config'
import {
  createCredential,
  credentialsQuery,
  revokeCredential,
  sortedByState,
  type Credential,
  type NewCredential,
} from '@/api/credentials'
import { sessionQuery } from '@/api/sidecar'
import { fieldClass } from '@/components/config/SettingsForm'
import { CopyButton } from '@/components/CopyButton'
import { StatusPill } from '@/components/StatusPill'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
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
import { formatSince, formatTime } from '@/lib/format'
import { credentialAddresses, rtcAddress, type Values } from '@/lib/quickSetup'
import { atLeast } from '@/lib/roles'
import { cn } from '@/lib/utils'

const actions = [
  { value: 'publish', label: 'Publish', hint: 'send a stream to MediaMTX' },
  { value: 'read', label: 'Read', hint: 'watch a stream' },
  { value: 'playback', label: 'Playback', hint: 'watch recordings' },
  { value: 'metrics', label: 'Metrics', hint: "scrape MediaMTX's metrics" },
] as const

const expiries = [
  { hours: 0, label: 'Never' },
  { hours: 24, label: 'In a day' },
  { hours: 24 * 7, label: 'In a week' },
  { hours: 24 * 30, label: 'In 30 days' },
  { hours: 24 * 365, label: 'In a year' },
] as const

const list = (s: string) =>
  s
    .split(',')
    .map((x) => x.trim())
    .filter(Boolean)

/** Stream credentials: create (the secret is shown once), list and revoke. */
export function CredentialsPage() {
  const { data: session } = useQuery(sessionQuery)
  const admin = atLeast(session?.user.role ?? 'viewer', 'admin')
  const creds = useQuery({ ...credentialsQuery, enabled: admin })
  const [creating, setCreating] = useState(false)
  const [created, setCreated] = useState<Credential | null>(null)

  if (!admin) {
    return (
      <div className="space-y-2">
        <h1 className="font-heading text-2xl font-semibold">Credentials</h1>
        <p className="text-sm text-muted-foreground">Stream credentials are for admins.</p>
      </div>
    )
  }
  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-end justify-between gap-3">
        <div className="space-y-1">
          <h1 className="font-heading text-2xl font-semibold">Credentials</h1>
          <p className="text-sm text-muted-foreground">
            What publishers and viewers give MediaMTX. Each one is limited to what it may do, where,
            from where and until when; revoking one also closes the streams it has open.
          </p>
        </div>
        {!creating && !created && (
          <Button
            onClick={() => {
              setCreating(true)
            }}
          >
            <Plus aria-hidden />
            New credential
          </Button>
        )}
      </div>
      {created && (
        <CreatedPanel
          credential={created}
          onClose={() => {
            setCreated(null)
          }}
        />
      )}
      {creating && (
        <CreateForm
          taken={(n) => creds.data?.some((c) => c.name === n) ?? false}
          onCancel={() => {
            setCreating(false)
          }}
          onCreated={(c) => {
            setCreating(false)
            setCreated(c)
          }}
        />
      )}
      {!creds.data ? (
        <Skeleton className="h-32 w-full" />
      ) : (
        <CredentialTable credentials={creds.data} />
      )}
    </div>
  )
}

function CreateForm({
  taken,
  onCancel,
  onCreated,
}: {
  taken: (name: string) => boolean
  onCancel: () => void
  onCreated: (c: Credential) => void
}) {
  const queryClient = useQueryClient()
  const [name, setName] = useState('')
  const [kind, setKind] = useState<NewCredential['kind']>('password')
  const [chosen, setChosen] = useState<string[]>(['read'])
  const [paths, setPaths] = useState('')
  const [sources, setSources] = useState('')
  const [expires, setExpires] = useState(0)
  const create = useMutation({
    mutationFn: createCredential,
    onSuccess: async (c) => {
      await queryClient.invalidateQueries({ queryKey: credentialsQuery.queryKey })
      onCreated(c)
    },
  })
  let problem: string | null = null
  if (!/^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/.test(name)) {
    problem = 'Names use letters, digits and . _ - (up to 64).'
  } else if (taken(name)) problem = 'A credential with this name exists.'
  else if (chosen.length === 0) problem = 'Choose at least one action.'
  return (
    <form
      className="max-w-3xl space-y-4 rounded-xl border bg-card p-4"
      onSubmit={(e) => {
        e.preventDefault()
        if (problem) return
        create.mutate({
          name,
          kind,
          actions: chosen,
          paths: list(paths),
          sources: list(sources),
          expiresInHours: expires,
        })
      }}
    >
      <p className="section-title">New credential</p>
      <Labeled
        id="cred-name"
        label="Name"
        hint="Clients use it as the user name; also how it shows in lists and logs."
      >
        <input
          id="cred-name"
          className={cn(fieldClass, 'font-mono')}
          value={name}
          onChange={(e) => {
            setName(e.target.value)
          }}
        />
      </Labeled>
      <Labeled id="cred-kind" label="Kind">
        <select
          id="cred-kind"
          className={fieldClass}
          value={kind}
          onChange={(e) => {
            setKind(e.target.value as NewCredential['kind'])
          }}
        >
          <option value="password">Name and secret (RTSP, RTMP, SRT and most clients)</option>
          <option value="token">Bearer token (HTTP clients: HLS, WebRTC, scripts)</option>
        </select>
      </Labeled>
      <fieldset className="space-y-2">
        <legend className="text-sm font-medium">May</legend>
        <div className="grid gap-2 sm:grid-cols-2">
          {actions.map((a) => (
            <label key={a.value} className="flex items-center gap-2 text-sm">
              <Checkbox
                checked={chosen.includes(a.value)}
                onCheckedChange={(on) => {
                  setChosen((c) => (on ? [...c, a.value] : c.filter((x) => x !== a.value)))
                }}
              />
              {a.label}
              <span className="text-muted-foreground">({a.hint})</span>
            </label>
          ))}
        </div>
      </fieldset>
      <Labeled
        id="cred-paths"
        label="Paths"
        hint="Comma-separated names, or ~regex. Empty: any path."
      >
        <input
          id="cred-paths"
          className={cn(fieldClass, 'font-mono')}
          placeholder="cam1, ~^studio/"
          value={paths}
          onChange={(e) => {
            setPaths(e.target.value)
          }}
        />
      </Labeled>
      <Labeled
        id="cred-sources"
        label="From"
        hint="Comma-separated IPs or networks. Empty: anywhere."
      >
        <input
          id="cred-sources"
          className={cn(fieldClass, 'font-mono')}
          placeholder="203.0.113.7, 192.168.1.0/24"
          value={sources}
          onChange={(e) => {
            setSources(e.target.value)
          }}
        />
      </Labeled>
      <Labeled id="cred-expires" label="Expires">
        <select
          id="cred-expires"
          className={fieldClass}
          value={expires}
          onChange={(e) => {
            setExpires(Number(e.target.value))
          }}
        >
          {expiries.map((x) => (
            <option key={x.hours} value={x.hours}>
              {x.label}
            </option>
          ))}
        </select>
      </Labeled>
      {create.error && (
        <Alert variant="destructive" role="alert">
          <AlertDescription>
            {create.error instanceof ApiError
              ? create.error.message
              : 'The credential could not be created.'}
          </AlertDescription>
        </Alert>
      )}
      {problem && name !== '' && (
        <p className="text-sm text-muted-foreground" data-testid="cred-problem">
          {problem}
        </p>
      )}
      <div className="flex gap-2">
        <Button type="submit" disabled={!!problem || create.isPending}>
          {create.isPending ? 'Creating…' : 'Create'}
        </Button>
        <Button type="button" variant="outline" onClick={onCancel}>
          Cancel
        </Button>
      </div>
    </form>
  )
}

function Labeled({
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

/** The one moment the secret is visible, with the addresses that carry it. */
function CreatedPanel({ credential: c, onClose }: { credential: Credential; onClose: () => void }) {
  const { data: config } = useQuery(configQuery)
  const secret = c.secret ?? ''
  const global = (config?.settings ?? {}) as Values
  const onePath =
    c.paths.length === 1 && !c.paths[0]?.startsWith('~') ? (c.paths[0] ?? '') : '<path>'
  const mode = c.actions.includes('publish') ? 'publish' : 'read'
  const rtc = config ? rtcAddress(window.location.origin, global, onePath, mode) : null
  const urls = [
    ...(c.kind === 'password' && config
      ? credentialAddresses(config.publicHost, global, onePath, c.name, secret, mode)
      : []),
    ...(rtc ? [rtc] : []),
  ]
  return (
    <div
      className="max-w-3xl space-y-4 rounded-xl border border-signal/50 bg-card p-4"
      data-testid="cred-created"
    >
      <p className="flex items-center gap-2 font-medium">
        <KeyRound className="size-4 text-signal" aria-hidden />
        {c.name} is ready. Copy the secret now: it is not shown again.
      </p>
      <div className="flex items-center gap-2">
        <code
          className="min-w-0 flex-1 rounded-lg border bg-background px-3 py-2 font-mono text-sm break-all select-all"
          data-testid="cred-secret"
        >
          {secret}
        </code>
        <CopyButton text={secret} label="secret" />
      </div>
      {c.kind === 'token' && (
        <p className="text-sm text-muted-foreground">
          Send it as <code className="font-mono">Authorization: Bearer {'<token>'}</code>.
        </p>
      )}
      {urls.length > 0 && (
        <div className="space-y-1.5">
          <p className="section-title">{mode === 'publish' ? 'Publish to' : 'Read from'}</p>
          <ul className="space-y-1" aria-label="Addresses with the credential">
            {urls.map((u) => (
              <li key={u.protocol} className="flex items-center gap-2 text-sm">
                <span className="w-14 shrink-0 text-muted-foreground">{u.protocol}</span>
                <code className="min-w-0 flex-1 font-mono text-xs break-all">{u.url}</code>
                <CopyButton text={u.url} label={`${u.protocol} address`} />
              </li>
            ))}
          </ul>
          {rtc && (
            <p className="text-xs text-muted-foreground" data-testid="cred-rtc-note">
              {c.kind === 'token'
                ? `For ${rtc.protocol}, enter the token as the bearer token (in OBS: Settings, Stream, service WHIP).`
                : `For ${rtc.protocol}, the client sends ${c.name} and the secret as HTTP Basic auth. OBS only sends a bearer token: create a token credential for it.`}
            </p>
          )}
          {onePath === '<path>' && (
            <p className="text-xs text-muted-foreground">
              Replace &lt;path&gt; with the path to use.
            </p>
          )}
        </div>
      )}
      <Button variant="outline" onClick={onClose}>
        I have copied it
      </Button>
    </div>
  )
}

function CredentialTable({ credentials }: { credentials: Credential[] }) {
  const queryClient = useQueryClient()
  const now = useNow()
  const [confirm, setConfirm] = useState<string | null>(null)
  const [note, setNote] = useState<string | null>(null)
  const revoke = useMutation({
    mutationFn: revokeCredential,
    onSuccess: async (res, name) => {
      setConfirm(null)
      setNote(
        `${name} is revoked${res.kicked > 0 ? `; ${String(res.kicked)} open ${res.kicked === 1 ? 'stream was' : 'streams were'} closed` : ''}.`,
      )
      await queryClient.invalidateQueries({ queryKey: credentialsQuery.queryKey })
    },
  })
  if (credentials.length === 0) {
    return (
      <p className="rounded-lg border border-dashed p-8 text-center text-sm text-muted-foreground">
        No credentials yet: nobody can publish or read until one exists.
      </p>
    )
  }
  return (
    <div className="space-y-2">
      {note && (
        <p role="status" className="text-sm" data-testid="cred-note">
          {note}
        </p>
      )}
      {revoke.error && (
        <Alert variant="destructive" role="alert">
          <AlertDescription>
            {revoke.error instanceof ApiError
              ? revoke.error.message
              : 'The credential could not be revoked.'}
          </AlertDescription>
        </Alert>
      )}
      <div className="rounded-lg border">
        <Table aria-label="Credentials">
          <TableHeader>
            <TableRow>
              <TableHead>Name</TableHead>
              <TableHead>May</TableHead>
              <TableHead className="hidden md:table-cell">Paths</TableHead>
              <TableHead className="hidden lg:table-cell">From</TableHead>
              <TableHead className="hidden md:table-cell">Expires</TableHead>
              <TableHead className="hidden lg:table-cell">Last used</TableHead>
              <TableHead>State</TableHead>
              <TableHead className="text-right">
                <span className="sr-only">Actions</span>
              </TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {sortedByState(credentials).map((c) => (
              <TableRow key={c.name} data-testid={`cred-${c.name}`}>
                <TableCell className="font-mono">
                  {c.name}
                  {c.kind === 'token' && (
                    <span className="ml-1.5 text-xs text-muted-foreground">(token)</span>
                  )}
                </TableCell>
                <TableCell>{c.actions.join(', ')}</TableCell>
                <TableCell className="hidden font-mono text-xs md:table-cell">
                  {c.paths.join(', ') || 'any'}
                </TableCell>
                <TableCell className="hidden font-mono text-xs lg:table-cell">
                  {c.sources.join(', ') || 'anywhere'}
                </TableCell>
                <TableCell className="hidden md:table-cell">
                  {c.expiresAt ? formatTime(c.expiresAt) : 'never'}
                </TableCell>
                <TableCell className="hidden lg:table-cell">
                  {c.lastUsedAt ? formatSince(c.lastUsedAt, now) : 'never'}
                </TableCell>
                <TableCell>
                  <StatusPill tone={c.state === 'active' ? 'good' : 'neutral'}>
                    {c.state}
                  </StatusPill>
                </TableCell>
                <TableCell className="text-right">
                  {c.state === 'revoked' ? null : confirm === c.name ? (
                    <span className="inline-flex gap-2">
                      <Button
                        size="sm"
                        variant="destructive"
                        disabled={revoke.isPending}
                        onClick={() => {
                          revoke.mutate(c.name)
                        }}
                      >
                        Revoke {c.name}
                      </Button>
                      <Button
                        size="sm"
                        variant="outline"
                        onClick={() => {
                          setConfirm(null)
                        }}
                      >
                        Keep
                      </Button>
                    </span>
                  ) : (
                    <Button
                      size="sm"
                      variant="ghost"
                      onClick={() => {
                        setConfirm(c.name)
                      }}
                    >
                      Revoke
                    </Button>
                  )}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </div>
    </div>
  )
}
