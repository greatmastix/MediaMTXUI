import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Plus, Send, Trash2 } from 'lucide-react'
import { useState } from 'react'

import { ApiError } from '@/api/client'
import {
  createForward,
  deleteForward,
  forwardsQuery,
  setForwardEnabled,
  type Forward,
  type Stream,
} from '@/api/streams'
import { fieldClass } from '@/components/config/SettingsForm'
import { StatusPill } from '@/components/StatusPill'
import { Button } from '@/components/ui/button'
import { formatBytes } from '@/lib/format'

// Forwarding: the stream sent on to other platforms while it is live here. Pick a platform, paste its
// stream key, switch it on and off. The key goes to the server once and never comes back.

const providers = [
  {
    id: 'twitch',
    label: 'Twitch',
    server: false,
    hint: 'Your key: Twitch Creator Dashboard → Settings → Stream → Primary Stream key.',
  },
  {
    id: 'youtube',
    label: 'YouTube',
    server: false,
    hint: 'Your key: YouTube Studio → Go live → Stream → Stream key.',
  },
  {
    id: 'kick',
    label: 'Kick',
    server: true,
    hint: 'Kick Creator Dashboard → Settings → Stream URL & Key: paste both.',
  },
  {
    id: 'custom',
    label: 'Custom',
    server: true,
    hint: 'An RTMP, RTMPS, SRT, RTSP or WHIP address. For RTMP, the stream key goes in the key field; for WHIP, the bearer token.',
  },
] as const

const providerLabel = (id: string) => providers.find((p) => p.id === id)?.label ?? id

function errorText(e: unknown, fallback: string) {
  return e instanceof ApiError ? e.message : fallback
}

function ForwardState({ f }: { f: Forward }) {
  switch (f.state) {
    case 'off':
      return <StatusPill tone="neutral">Off</StatusPill>
    case 'idle':
      return <StatusPill tone="neutral">On · starts when you go live</StatusPill>
    case 'forwarding':
      return <StatusPill tone="good">Forwarding · {formatBytes(f.outboundBytes)} sent</StatusPill>
    case 'error':
      return <StatusPill tone="warning">Not getting through</StatusPill>
    case 'missing':
      return <StatusPill tone="warning">On, but not in MediaMTX</StatusPill>
    default:
      return <StatusPill tone="neutral">On · state unknown</StatusPill>
  }
}

export function Forwarding({ stream: s }: { stream: Stream }) {
  const queryClient = useQueryClient()
  const forwards = useQuery(forwardsQuery(s.id))
  const [adding, setAdding] = useState(false)
  const refresh = () => queryClient.invalidateQueries({ queryKey: forwardsQuery(s.id).queryKey })
  const toggle = useMutation({
    mutationFn: (f: Forward) => setForwardEnabled(s.id, f.id, !f.enabled),
    onSuccess: refresh,
  })
  const remove = useMutation({
    mutationFn: (f: Forward) => deleteForward(s.id, f.id),
    onSuccess: refresh,
  })
  const [confirm, setConfirm] = useState<number | null>(null)
  const list = forwards.data ?? []
  return (
    <section className="space-y-3 rounded-xl border bg-card p-4" aria-labelledby="forwarding-title">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h2 id="forwarding-title" className="flex items-center gap-2 font-medium">
          <Send className="size-4 text-signal" aria-hidden /> Forwarding
        </h2>
        {!adding && list.length < 5 && (
          <Button
            variant="outline"
            size="sm"
            onClick={() => {
              setAdding(true)
            }}
          >
            <Plus /> Add a platform
          </Button>
        )}
      </div>
      <p className="text-sm text-muted-foreground">
        Send the stream on to Twitch, YouTube, Kick or another server while you stream here, with no
        extra upload from you. Check each platform&apos;s rules on streaming to several places at
        once.
      </p>
      {adding && (
        <AddForward
          stream={s}
          onDone={() => {
            setAdding(false)
            void refresh()
          }}
        />
      )}
      {list.length > 0 && (
        <ul className="divide-y rounded-lg border" aria-label="Forwards">
          {list.map((f) => (
            <li key={f.id} className="space-y-1.5 p-3" data-testid={`forward-${String(f.id)}`}>
              <div className="flex flex-wrap items-center justify-between gap-2">
                <div className="min-w-0">
                  <p className="font-medium">{providerLabel(f.provider)}</p>
                  <p className="truncate font-mono text-xs text-muted-foreground">{f.label}</p>
                </div>
                <div className="flex flex-wrap items-center gap-2">
                  <span data-testid="forward-state" data-state={f.state}>
                    <ForwardState f={f} />
                  </span>
                  <Button
                    variant="outline"
                    size="sm"
                    disabled={toggle.isPending}
                    onClick={() => {
                      toggle.mutate(f)
                    }}
                  >
                    {f.enabled ? 'Switch off' : 'Switch on'}
                  </Button>
                  {confirm === f.id ? (
                    <>
                      <Button
                        variant="destructive"
                        size="sm"
                        disabled={remove.isPending}
                        onClick={() => {
                          remove.mutate(f)
                          setConfirm(null)
                        }}
                      >
                        Remove
                      </Button>
                      <Button
                        variant="outline"
                        size="sm"
                        onClick={() => {
                          setConfirm(null)
                        }}
                      >
                        Cancel
                      </Button>
                    </>
                  ) : (
                    <Button
                      variant="ghost"
                      size="sm"
                      aria-label={`Remove ${providerLabel(f.provider)}`}
                      onClick={() => {
                        setConfirm(f.id)
                      }}
                    >
                      <Trash2 />
                    </Button>
                  )}
                </div>
              </div>
              {f.state === 'error' && f.lastError && (
                <p className="text-xs text-muted-foreground">
                  {f.lastError}. Check the key; MediaMTX keeps trying every few seconds.
                </p>
              )}
              {f.state === 'missing' && (
                <p className="text-xs text-muted-foreground">
                  MediaMTX&apos;s configuration no longer has this destination (it was edited
                  elsewhere). Switch it off and on again.
                </p>
              )}
            </li>
          ))}
        </ul>
      )}
      {(toggle.error ?? remove.error) && (
        <p className="text-sm text-destructive" role="alert">
          {errorText(toggle.error ?? remove.error, 'That did not work.')}
        </p>
      )}
      {list.length > 0 && (
        <p className="text-xs text-muted-foreground">
          Keys are stored encrypted and never shown again. While forwarding is on, the key is also
          in MediaMTX&apos;s configuration, which admins of this server can see.
        </p>
      )}
    </section>
  )
}

function AddForward({ stream: s, onDone }: { stream: Stream; onDone: () => void }) {
  const [provider, setProvider] = useState<(typeof providers)[number]['id']>('twitch')
  const [server, setServer] = useState('')
  const [key, setKey] = useState('')
  const [enabled, setEnabled] = useState(true)
  const p = providers.find((x) => x.id === provider) ?? providers[0]
  const create = useMutation({
    mutationFn: () =>
      createForward(s.id, { provider, server: server.trim(), key: key.trim(), enabled }),
    onSuccess: onDone,
  })
  return (
    <form
      className="space-y-3 rounded-lg border p-3"
      aria-label="Add a platform"
      onSubmit={(e) => {
        e.preventDefault()
        create.mutate()
      }}
    >
      <div className="grid gap-3 sm:grid-cols-2">
        <label className="space-y-1 text-sm">
          <span className="font-medium">Platform</span>
          <select
            className={fieldClass}
            value={provider}
            onChange={(e) => {
              setProvider(e.target.value as typeof provider)
            }}
          >
            {providers.map((x) => (
              <option key={x.id} value={x.id}>
                {x.label}
              </option>
            ))}
          </select>
        </label>
        {p.server && (
          <label className="space-y-1 text-sm">
            <span className="font-medium">Server</span>
            <input
              className={fieldClass}
              value={server}
              required={provider === 'kick'}
              placeholder={provider === 'kick' ? 'rtmps://….live-video.net' : 'rtmp://host/app'}
              onChange={(e) => {
                setServer(e.target.value)
              }}
            />
          </label>
        )}
        <label className="space-y-1 text-sm sm:col-span-2">
          <span className="font-medium">Stream key</span>
          <input
            className={fieldClass}
            type="password"
            autoComplete="off"
            value={key}
            required={provider !== 'custom'}
            onChange={(e) => {
              setKey(e.target.value)
            }}
          />
          <span className="block text-xs text-muted-foreground">{p.hint}</span>
        </label>
        <label className="flex items-center gap-2 text-sm sm:col-span-2">
          <input
            type="checkbox"
            checked={enabled}
            onChange={(e) => {
              setEnabled(e.target.checked)
            }}
          />
          Switch it on now
        </label>
      </div>
      {create.error && (
        <p className="text-sm text-destructive" role="alert">
          {errorText(create.error, 'The platform was not added.')}
        </p>
      )}
      <div className="flex gap-2">
        <Button type="submit" disabled={create.isPending}>
          Add
        </Button>
        <Button type="button" variant="outline" onClick={onDone}>
          Cancel
        </Button>
      </div>
    </form>
  )
}
