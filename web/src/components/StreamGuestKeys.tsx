import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Plus, UserPlus } from 'lucide-react'
import { useState } from 'react'

import { ApiError } from '@/api/client'
import {
  createGuestKey,
  guestKeysQuery,
  revokeGuestKey,
  type GuestKey,
  type Stream,
} from '@/api/streams'
import { fieldClass } from '@/components/config/SettingsForm'
import { StatusPill } from '@/components/StatusPill'
import { TargetList } from '@/components/TargetList'
import { Button } from '@/components/ui/button'
import { useNow } from '@/hooks/useNow'
import { formatSince, formatTime } from '@/lib/format'
import { playbackTargets, publishTargets } from '@/lib/streamAddresses'

// Guest keys: let someone stream to this stream for a while (a co-host, a stand-in) or watch it while
// it is private, without an account. A key works for this stream only, stops by itself, and can be revoked at once.
// Its secret is shown once, as the addresses the guest needs.

const durations = [
  { hours: 1, label: '1 hour' },
  { hours: 6, label: '6 hours' },
  { hours: 24, label: '1 day' },
  { hours: 72, label: '3 days' },
  { hours: 168, label: '1 week' },
]

function errorText(e: unknown, fallback: string) {
  return e instanceof ApiError ? e.message : fallback
}

export function GuestKeys({ stream: s }: { stream: Stream }) {
  const queryClient = useQueryClient()
  const keys = useQuery(guestKeysQuery(s.id))
  const [adding, setAdding] = useState(false)
  const [made, setMade] = useState<{ guest: GuestKey; secret: string } | null>(null)
  const refresh = () => {
    void queryClient.invalidateQueries({ queryKey: guestKeysQuery(s.id).queryKey })
  }
  const list = keys.data ?? []
  const valid = list.filter((k) => k.state === 'active')
  const past = list.filter((k) => k.state !== 'active').slice(0, 5)
  return (
    <section className="space-y-3 rounded-xl border bg-card p-4" aria-labelledby="guests-title">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h2 id="guests-title" className="flex items-center gap-2 font-medium">
          <UserPlus className="size-4 text-signal" aria-hidden /> Guest keys
        </h2>
        {!adding && !made && (
          <Button
            variant="outline"
            size="sm"
            onClick={() => {
              setAdding(true)
            }}
          >
            <Plus /> New guest key
          </Button>
        )}
      </div>
      <p className="text-sm text-muted-foreground">
        Let someone stream here for a while (a co-host, a stand-in), or watch while the stream is
        private, without an account. A guest key works for this stream only and stops by itself.
      </p>
      {adding && (
        <NewGuestKey
          stream={s}
          onCancel={() => {
            setAdding(false)
          }}
          onMade={(m) => {
            setAdding(false)
            setMade(m)
            refresh()
          }}
        />
      )}
      {made && (
        <MadeKey
          stream={s}
          made={made}
          onDone={() => {
            setMade(null)
          }}
        />
      )}
      {valid.length > 0 && (
        <ul className="divide-y rounded-lg border" aria-label="Valid guest keys">
          {valid.map((k) => (
            <GuestRow key={k.id} stream={s} guest={k} onChanged={refresh} />
          ))}
        </ul>
      )}
      {past.length > 0 && (
        <details className="text-sm">
          <summary className="cursor-pointer text-muted-foreground">
            Expired and revoked ({past.length})
          </summary>
          <ul className="mt-2 divide-y rounded-lg border" aria-label="Past guest keys">
            {past.map((k) => (
              <GuestRow key={k.id} stream={s} guest={k} onChanged={refresh} />
            ))}
          </ul>
        </details>
      )}
    </section>
  )
}

function GuestRow({
  stream: s,
  guest: k,
  onChanged,
}: {
  stream: Stream
  guest: GuestKey
  onChanged: () => void
}) {
  const now = useNow(60_000)
  const [confirm, setConfirm] = useState(false)
  const revoke = useMutation({
    mutationFn: () => revokeGuestKey(s.id, k.id),
    onSuccess: onChanged,
  })
  return (
    <li className="space-y-1 p-3" data-testid={`guest-${k.name}`}>
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="min-w-0">
          <p className="font-medium">
            {k.label}{' '}
            <span className="font-normal text-muted-foreground">
              · {k.kind === 'publish' ? 'may stream here' : 'may watch'}
            </span>
          </p>
          <p className="font-mono text-xs text-muted-foreground">{k.name}</p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          {k.state === 'active' ? (
            <StatusPill tone="good">Valid until {formatTime(k.expiresAt)}</StatusPill>
          ) : (
            <StatusPill tone="neutral">{k.state === 'expired' ? 'Expired' : 'Revoked'}</StatusPill>
          )}
          {k.state === 'active' &&
            (confirm ? (
              <>
                <Button
                  variant="destructive"
                  size="sm"
                  disabled={revoke.isPending}
                  onClick={() => {
                    revoke.mutate()
                  }}
                >
                  Revoke now
                </Button>
                <Button
                  variant="outline"
                  size="sm"
                  onClick={() => {
                    setConfirm(false)
                  }}
                >
                  Cancel
                </Button>
              </>
            ) : (
              <Button
                variant="outline"
                size="sm"
                onClick={() => {
                  setConfirm(true)
                }}
              >
                Revoke
              </Button>
            ))}
        </div>
      </div>
      <p className="text-xs text-muted-foreground">
        Made by {k.createdBy} {formatSince(k.createdAt, now)}
        {k.lastUsedAt ? `, last used ${formatSince(k.lastUsedAt, now)}` : ', not used yet'}.
        {confirm && ' Revoking disconnects the guest at once.'}
      </p>
      {revoke.error && (
        <p className="text-sm text-destructive" role="alert">
          {errorText(revoke.error, 'The key was not revoked.')}
        </p>
      )}
    </li>
  )
}

function NewGuestKey({
  stream: s,
  onCancel,
  onMade,
}: {
  stream: Stream
  onCancel: () => void
  onMade: (m: { guest: GuestKey; secret: string }) => void
}) {
  const [label, setLabel] = useState('')
  const [kind, setKind] = useState<GuestKey['kind']>('publish')
  const [hours, setHours] = useState(6)
  const create = useMutation({
    mutationFn: () => createGuestKey(s.id, { kind, label: label.trim(), hours }),
    onSuccess: onMade,
  })
  return (
    <form
      className="grid gap-3 rounded-lg border p-3 sm:grid-cols-3"
      aria-label="New guest key"
      onSubmit={(e) => {
        e.preventDefault()
        create.mutate()
      }}
    >
      <label className="space-y-1 text-sm">
        <span className="font-medium">For</span>
        <input
          className={fieldClass}
          value={label}
          required
          maxLength={60}
          placeholder="Bob (co-host)"
          onChange={(e) => {
            setLabel(e.target.value)
          }}
        />
      </label>
      <label className="space-y-1 text-sm">
        <span className="font-medium">Lets them</span>
        <select
          className={fieldClass}
          value={kind}
          onChange={(e) => {
            setKind(e.target.value as GuestKey['kind'])
          }}
        >
          <option value="publish">Stream here</option>
          <option value="read">Watch</option>
        </select>
      </label>
      <label className="space-y-1 text-sm">
        <span className="font-medium">Valid for</span>
        <select
          className={fieldClass}
          value={hours}
          onChange={(e) => {
            setHours(Number(e.target.value))
          }}
        >
          {durations.map((d) => (
            <option key={d.hours} value={d.hours}>
              {d.label}
            </option>
          ))}
        </select>
      </label>
      {kind === 'publish' && (
        <p className="text-xs text-muted-foreground sm:col-span-3">
          While the key is valid, the server&apos;s streaming ports stay open, so the guest&apos;s
          encoder gets in from anywhere; only someone with the key can stream. A guest streaming
          takes over from whoever streams here at the time.
        </p>
      )}
      {create.error && (
        <p className="text-sm text-destructive sm:col-span-3" role="alert">
          {errorText(create.error, 'The guest key was not made.')}
        </p>
      )}
      <div className="flex gap-2 sm:col-span-3">
        <Button type="submit" disabled={create.isPending}>
          Make the key
        </Button>
        <Button type="button" variant="outline" onClick={onCancel}>
          Cancel
        </Button>
      </div>
    </form>
  )
}

/** The new key's addresses, shown once: what to send the guest. */
function MadeKey({
  stream: s,
  made,
  onDone,
}: {
  stream: Stream
  made: { guest: GuestKey; secret: string }
  onDone: () => void
}) {
  const g = made.guest
  const key = {
    kind: g.kind === 'publish' ? ('publish' as const) : ('playback' as const),
    name: g.name,
    secret: made.secret,
  }
  const origin = window.location.origin
  const targets =
    g.kind === 'publish' ? publishTargets(s, key, origin) : playbackTargets(s, key, origin)
  return (
    <div
      className="space-y-3 rounded-lg border border-signal/40 p-3"
      role="region"
      aria-label="New guest key"
      data-testid="guest-made"
    >
      <p className="text-sm">
        <span className="font-medium">Send these to {g.label}.</span>{' '}
        <span className="text-muted-foreground">
          They are shown only now; the key works until {formatTime(g.expiresAt)}. Send them
          privately: anyone with them can {g.kind === 'publish' ? 'stream here' : 'watch'}.
        </span>
      </p>
      {targets.length === 0 ? (
        <p className="text-sm text-muted-foreground">No protocol is switched on in MediaMTX.</p>
      ) : (
        <TargetList targets={targets} secret={made.secret} shown prefix="guest" />
      )}
      <Button size="sm" onClick={onDone}>
        Done
      </Button>
    </div>
  )
}
