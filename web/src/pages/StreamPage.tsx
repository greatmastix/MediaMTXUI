import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link, useNavigate } from '@tanstack/react-router'
import { ArrowLeft, Eye, EyeOff, KeyRound, Radio, RefreshCw, Unplug } from 'lucide-react'
import { useState } from 'react'

import { ApiError } from '@/api/client'
import { peopleQuery } from '@/api/people'
import { sessionQuery } from '@/api/sidecar'
import {
  deleteStream,
  disconnectStream,
  leaseStream,
  regenerateKey,
  revealKey,
  streamHistoryQuery,
  streamQuery,
  streamsQuery,
  updateStream,
  type KeyKind,
  type PathSample,
  type Stream,
  type StreamKey,
} from '@/api/streams'
import { rangeMs, rangeStepMs, type HistoryRange } from '@/api/history'
import { fieldClass } from '@/components/config/SettingsForm'
import { CopyButton } from '@/components/CopyButton'
import { Player } from '@/components/Player'
import { StatusPill } from '@/components/StatusPill'
import { EncoderNotes } from '@/components/EncoderNotes'
import { Forwarding } from '@/components/StreamForwarding'
import { GuestKeys } from '@/components/StreamGuestKeys'
import { Holding } from '@/components/StreamHolding'
import { Guidance, Health } from '@/components/StreamGuidance'
import { TargetList } from '@/components/TargetList'
import { RangePicker } from '@/components/RangePicker'
import { TimeSeriesChart } from '@/components/TimeSeriesChart'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { useNow } from '@/hooks/useNow'
import { formatBitrate, formatSince } from '@/lib/format'
import { atLeast } from '@/lib/roles'
import { playbackTargets, publicTargets, publishTargets, watchLink } from '@/lib/streamAddresses'
import { useStreamLive } from '@/live/useStreamLive'

// One stream, in plain words: is it live, how to go live (server and key for the encoder), a preview, who is
// watching and how steady the bitrate is, and its settings. Keys stay hidden until asked for; showing one is audited.

function errorText(e: unknown, fallback: string) {
  return e instanceof ApiError ? e.message : fallback
}

export function StreamPage({ id }: { id: number }) {
  const stream = useQuery(streamQuery(id))
  if (stream.error) {
    return (
      <Alert variant="destructive" role="alert">
        <AlertDescription>
          {errorText(stream.error, 'This stream cannot be shown.')}
        </AlertDescription>
      </Alert>
    )
  }
  if (!stream.data) return <Skeleton className="h-64 w-full max-w-4xl" />
  return <StreamView stream={stream.data} />
}

function StreamView({ stream: s }: { stream: Stream }) {
  const { online, available, viewers, inBps, ratesAt, path } = useStreamLive(s.name)
  const tracks = path?.tracks2 ?? []
  const { data: session } = useQuery(sessionQuery)
  const expert = atLeast(session?.user.role ?? 'streamer', 'viewer')
  return (
    <div className="max-w-5xl space-y-6">
      <div className="space-y-1">
        <Link
          to="/streams"
          className="inline-flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground"
        >
          <ArrowLeft className="size-4" aria-hidden /> Streams
        </Link>
        <div className="flex flex-wrap items-center gap-3">
          <h1 className="font-heading text-2xl font-semibold">{s.title}</h1>
          {online ? (
            <StatusPill tone="good" data-testid="stream-state" data-state="live">
              <Radio className="size-3 text-signal" aria-hidden /> Live
            </StatusPill>
          ) : (
            <StatusPill tone="neutral" data-testid="stream-state" data-state="offline">
              Offline
            </StatusPill>
          )}
          {expert && (
            <Link
              to="/paths/$"
              params={{ _splat: s.name }}
              className="text-sm text-muted-foreground underline-offset-4 hover:text-foreground hover:underline"
            >
              Path details
            </Link>
          )}
          {expert && (
            <Link
              to="/recordings/$"
              params={{ _splat: s.name }}
              className="text-sm text-muted-foreground underline-offset-4 hover:text-foreground hover:underline"
            >
              Recordings
            </Link>
          )}
        </div>
        <p className="text-sm text-muted-foreground">
          <span className="font-mono">{s.name}</span>
          {s.owner ? ` · streamed by ${s.owner.username}` : ''}
          {online && (
            <span className="tabular-nums" data-testid="stream-numbers">
              {' '}
              · {viewers} watching · {inBps !== undefined ? formatBitrate(inBps) : '…'} in
              {tracks.length > 0 &&
                ` · ${tracks
                  .map((t) => ('codec' in t ? String(t.codec) : ''))
                  .filter(Boolean)
                  .join(' + ')}`}
            </span>
          )}
        </p>
      </div>

      {s.canManage && <EncoderNotes stream={s} />}
      {s.canManage && online && (
        <Health stream={s} tracks={tracks} inBps={inBps} ratesAt={ratesAt} />
      )}
      {s.canManage && <GoLive stream={s} />}
      {s.canManage && <Guidance stream={s} />}
      {s.canManage && <Forwarding stream={s} />}
      {s.canManage && <GuestKeys stream={s} />}
      {s.canManage && (
        <Holding stream={s} online={online} viewers={viewers} available={available} />
      )}
      {s.public && <PublicOutputs stream={s} />}
      {s.canManage && <Outputs stream={s} />}

      <section className="space-y-2" aria-labelledby="preview-title">
        <h2 id="preview-title" className="section-title">
          Preview
        </h2>
        {online || available ? (
          <Player path={s.name} stats className="max-w-2xl" />
        ) : (
          <p className="max-w-2xl rounded-lg border border-dashed p-8 text-center text-sm text-muted-foreground">
            Start streaming and the preview appears here by itself.
          </p>
        )}
      </section>

      <Activity stream={s} viewers={viewers} inBps={inBps} ratesAt={ratesAt} />

      {s.canManage && <Settings stream={s} online={online} />}
    </div>
  )
}

/** The encoder settings: server and stream key per protocol, hidden until shown. */
function GoLive({ stream: s }: { stream: Stream }) {
  const queryClient = useQueryClient()
  // While this page is open, the stream's publishing ports stay open to this browser's address (automatic exposure),
  // so an encoder on the same network gets in. Renewed every minute; it lapses a few minutes after the page closes.
  const lease = useQuery({
    queryKey: ['streams', s.id, 'lease'],
    queryFn: () => leaseStream(s.id),
    refetchInterval: 60_000,
    refetchIntervalInBackground: true,
    staleTime: 0,
  })
  const [key, setKey] = useState<StreamKey | null>(null)
  const [shown, setShown] = useState(false)
  const [confirm, setConfirm] = useState(false)
  const reveal = useMutation({
    mutationFn: (kind: KeyKind) => revealKey(s.id, kind),
    onSuccess: setKey,
  })
  const regenerate = useMutation({
    mutationFn: (kind: KeyKind) => regenerateKey(s.id, kind),
    onSuccess: async (k) => {
      setKey(k)
      setConfirm(false)
      await queryClient.invalidateQueries({ queryKey: streamQuery(s.id).queryKey })
    },
  })
  const now = useNow(60_000)
  const targets = key ? publishTargets(s, key, window.location.origin) : []
  return (
    <section className="space-y-3 rounded-xl border bg-card p-4" aria-labelledby="golive-title">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h2 id="golive-title" className="flex items-center gap-2 font-medium">
          <KeyRound className="size-4 text-signal" aria-hidden /> Go live
        </h2>
        {key && (
          <div className="flex gap-2">
            <Button
              variant="outline"
              size="sm"
              onClick={() => {
                setShown(!shown)
              }}
            >
              {shown ? <EyeOff /> : <Eye />} {shown ? 'Hide' : 'Show'} key
            </Button>
            <Button
              variant="outline"
              size="sm"
              onClick={() => {
                setConfirm(true)
              }}
            >
              <RefreshCw /> New key
            </Button>
          </div>
        )}
      </div>
      {!key ? (
        <div className="space-y-2 text-sm">
          <p className="text-muted-foreground">
            Your encoder needs a server address and a stream key. The key is like a password: anyone
            who has it can stream here as you.
          </p>
          <Button
            onClick={() => {
              reveal.mutate('publish')
            }}
            disabled={reveal.isPending || !s.keys.publish}
          >
            <Eye /> Show stream settings
          </Button>
          {!s.keys.publish && (
            <p className="text-sm text-muted-foreground">
              This stream has no key yet.{' '}
              <Button
                variant="link"
                size="sm"
                className="h-auto p-0"
                onClick={() => {
                  regenerate.mutate('publish')
                }}
              >
                Make one
              </Button>
            </p>
          )}
        </div>
      ) : targets.length === 0 ? (
        <p className="text-sm text-muted-foreground">
          No publishing protocol is switched on in MediaMTX. An admin can turn RTMP or SRT on in
          Configuration.
        </p>
      ) : (
        <TargetList targets={targets} secret={key.secret} shown={shown} prefix="golive" />
      )}
      {confirm && (
        <div className="flex flex-wrap items-center gap-2 rounded-lg border border-destructive/40 p-3 text-sm">
          <span>
            A new key stops the old one at once: an encoder streaming with it is disconnected and
            needs the new key.
          </span>
          <Button
            variant="destructive"
            size="sm"
            onClick={() => {
              regenerate.mutate('publish')
            }}
            disabled={regenerate.isPending}
          >
            Make a new key
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
        </div>
      )}
      {(reveal.error ?? regenerate.error) && (
        <p className="text-sm text-destructive" role="alert">
          {errorText(reveal.error ?? regenerate.error, 'The key could not be loaded.')}
        </p>
      )}
      {lease.data && !lease.data.off && (
        <p className="text-xs text-muted-foreground" data-testid="golive-lease">
          {lease.data.leased
            ? `While this page is open, the server accepts encoders from your address (${lease.data.address}); after a successful stream it remembers that address for 30 days.`
            : `${lease.data.reason ?? ''} The ports may be closed to your encoder; an admin can open them on the Exposure page.`}
        </p>
      )}
      {s.keys.publish && (
        <p className="text-xs text-muted-foreground">
          Key {s.keys.publish.name}, made {formatSince(s.keys.publish.createdAt, now)}
          {s.keys.publish.lastUsedAt
            ? `, last used ${formatSince(s.keys.publish.lastUsedAt, now)}`
            : ', not used yet'}
          .
        </p>
      )}
    </section>
  )
}

/** Bitrate in and viewers: the last hour from the sidecar's history, extended by the event stream's rates, or a longer
 * range from the stored minutes. */
function Activity({
  stream: s,
  viewers,
  inBps,
  ratesAt,
}: {
  stream: Stream
  viewers: number
  inBps: number | undefined
  ratesAt: number
}) {
  const [range, setRange] = useState<HistoryRange>('1h')
  const history = useQuery(streamHistoryQuery(s.id))
  const stored = useQuery({ ...streamHistoryQuery(s.id, range), enabled: range !== '1h' })
  // Each new rates event adds a point (React's "adjust state while rendering": no effect, no clock read).
  const [live, setLive] = useState<PathSample[]>([])
  const [seen, setSeen] = useState(ratesAt)
  if (ratesAt !== seen) {
    setSeen(ratesAt)
    if (inBps !== undefined) {
      setLive([...live.slice(-720), { t: ratesAt, inBps, outBps: 0, readers: viewers }])
    }
  }
  const now = useNow(5000)
  const samples = range === '1h' ? [...(history.data ?? []), ...live] : (stored.data ?? [])
  const times = samples.map((x) => x.t)
  const windowMs = range === '1h' ? undefined : rangeMs(range)
  const gapMs = rangeStepMs[range] * 2.5
  return (
    <section className="space-y-3" aria-label="Activity">
      <div className="flex justify-end">
        <RangePicker value={range} onChange={setRange} />
      </div>
      <div className="grid gap-4 md:grid-cols-2">
        <TimeSeriesChart
          title="Bitrate in"
          times={times}
          series={[
            { label: 'In', className: 'text-signal', value: (i) => samples[i]?.inBps ?? null },
          ]}
          format={formatBitrate}
          now={now}
          windowMs={windowMs}
          gapMs={gapMs}
        />
        <TimeSeriesChart
          title="Viewers"
          times={times}
          series={[
            {
              label: 'Viewers',
              className: 'text-good',
              value: (i) => samples[i]?.readers ?? null,
            },
          ]}
          format={(v) => String(Math.round(v))}
          now={now}
          windowMs={windowMs}
          gapMs={gapMs}
        />
      </div>
    </section>
  )
}

function Settings({ stream: s, online }: { stream: Stream; online: boolean }) {
  const queryClient = useQueryClient()
  const navigate = useNavigate()
  const people = useQuery({ ...peopleQuery, enabled: s.canAdmin })
  const [title, setTitle] = useState(s.title)
  const [cap, setCap] = useState(String(s.maxReaders))
  const [isPublic, setPublic] = useState(s.public)
  const [record, setRecord] = useState(s.record)
  const [owner, setOwner] = useState(s.owner ? String(s.owner.id) : '0')
  const [confirmDelete, setConfirmDelete] = useState(false)
  // Just the list and this stream (exact keys: not its lease and history below them), without waiting for them.
  const refresh = () => {
    void queryClient.invalidateQueries({ queryKey: streamsQuery.queryKey, exact: true })
    void queryClient.invalidateQueries({ queryKey: streamQuery(s.id).queryKey, exact: true })
  }
  const save = useMutation({
    mutationFn: () =>
      updateStream(s.id, {
        title,
        public: isPublic,
        record,
        maxReaders: Number(cap) || 0,
        ...(s.canAdmin ? { ownerId: Number(owner) } : {}),
      }),
    onSuccess: refresh,
  })
  const disconnect = useMutation({ mutationFn: () => disconnectStream(s.id) })
  const remove = useMutation({
    mutationFn: () => deleteStream(s.id),
    onSuccess: async () => {
      // Leave first: refreshing this stream's own query now would only find it gone (404).
      await navigate({ to: '/streams' })
      queryClient.removeQueries({ queryKey: streamQuery(s.id).queryKey })
      void queryClient.invalidateQueries({ queryKey: streamsQuery.queryKey, exact: true })
    },
  })
  return (
    <section className="space-y-3 rounded-xl border bg-card p-4" aria-labelledby="settings-title">
      <h2 id="settings-title" className="font-medium">
        Settings
      </h2>
      <form
        className="grid gap-3 sm:grid-cols-3"
        onSubmit={(e) => {
          e.preventDefault()
          save.mutate()
        }}
      >
        <label className="space-y-1 text-sm">
          <span className="font-medium">Title</span>
          <input
            className={fieldClass}
            value={title}
            onChange={(e) => {
              setTitle(e.target.value)
            }}
          />
        </label>
        <label className="space-y-1 text-sm">
          <span className="font-medium">Viewer limit</span>
          <input
            className={fieldClass}
            type="number"
            min={0}
            value={cap}
            onChange={(e) => {
              setCap(e.target.value)
            }}
          />
          <span className="block text-xs text-muted-foreground">0 means no limit.</span>
        </label>
        <label className="flex items-start gap-2 text-sm sm:col-span-3">
          <input
            type="checkbox"
            className="mt-1"
            checked={isPublic}
            onChange={(e) => {
              setPublic(e.target.checked)
            }}
          />
          <span>
            <span className="font-medium">Public</span>
            <span className="block text-xs text-muted-foreground">
              Anyone can watch with the watch link or a keyless address. Streaming to it always
              needs the stream key.
            </span>
          </span>
        </label>
        <label className="flex items-start gap-2 text-sm sm:col-span-3">
          <input
            type="checkbox"
            className="mt-1"
            checked={record}
            onChange={(e) => {
              setRecord(e.target.checked)
            }}
          />
          <span>
            <span className="font-medium">Record this stream</span>
            <span className="block text-xs text-muted-foreground">
              MediaMTX keeps what is streamed here (not the holding screen), within the
              server&apos;s storage budget; the oldest recordings go first.
            </span>
          </span>
        </label>
        {s.canAdmin && (
          <label className="space-y-1 text-sm">
            <span className="font-medium">Owner</span>
            <select
              className={fieldClass}
              value={owner}
              onChange={(e) => {
                setOwner(e.target.value)
              }}
            >
              <option value="0">Nobody</option>
              {people.data?.map((p) => (
                <option key={p.id} value={p.id}>
                  {p.username} ({p.role})
                </option>
              ))}
            </select>
          </label>
        )}
        <div className="flex flex-wrap gap-2 sm:col-span-3">
          <Button type="submit" disabled={save.isPending}>
            Save
          </Button>
          {online && (
            <Button
              type="button"
              variant="outline"
              onClick={() => {
                disconnect.mutate()
              }}
              disabled={disconnect.isPending}
            >
              <Unplug /> Disconnect the encoder
            </Button>
          )}
          {s.canAdmin &&
            (confirmDelete ? (
              <>
                <Button
                  type="button"
                  variant="destructive"
                  onClick={() => {
                    remove.mutate()
                  }}
                >
                  Delete {s.name} and its keys
                </Button>
                <Button
                  type="button"
                  variant="outline"
                  onClick={() => {
                    setConfirmDelete(false)
                  }}
                >
                  Cancel
                </Button>
              </>
            ) : (
              <Button
                type="button"
                variant="outline"
                onClick={() => {
                  setConfirmDelete(true)
                }}
              >
                Delete stream
              </Button>
            ))}
        </div>
      </form>
      {save.isSuccess && (
        <p role="status" className="text-sm">
          Saved.
        </p>
      )}
      {disconnect.isSuccess && (
        <p role="status" className="text-sm">
          The encoder was disconnected. It can reconnect with the same key; make a new key to keep
          it out.
        </p>
      )}
      {(save.error ?? disconnect.error ?? remove.error) && (
        <p role="alert" className="text-sm text-destructive">
          {errorText(save.error ?? disconnect.error ?? remove.error, 'That did not work.')}
        </p>
      )}
    </section>
  )
}

/** Where players and other servers can watch the stream, with its playback key (separate from the stream key, so
 * sharing a player address never lets anyone stream). */
function Outputs({ stream: s }: { stream: Stream }) {
  const queryClient = useQueryClient()
  const [key, setKey] = useState<StreamKey | null>(null)
  const [shown, setShown] = useState(false)
  const [confirm, setConfirm] = useState(false)
  const reveal = useMutation({ mutationFn: () => revealKey(s.id, 'playback'), onSuccess: setKey })
  const regenerate = useMutation({
    mutationFn: () => regenerateKey(s.id, 'playback'),
    onSuccess: async (k) => {
      setKey(k)
      setConfirm(false)
      await queryClient.invalidateQueries({ queryKey: streamQuery(s.id).queryKey })
    },
  })
  const targets = key ? playbackTargets(s, key, window.location.origin) : []
  return (
    <section className="space-y-3 rounded-xl border bg-card p-4" aria-labelledby="outputs-title">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h2 id="outputs-title" className="font-medium">
          {s.public ? 'Outputs with the playback key' : 'Outputs'}
        </h2>
        {key && (
          <div className="flex gap-2">
            <Button
              variant="outline"
              size="sm"
              onClick={() => {
                setShown(!shown)
              }}
            >
              {shown ? <EyeOff /> : <Eye />} {shown ? 'Hide' : 'Show'} key
            </Button>
            <Button
              variant="outline"
              size="sm"
              onClick={() => {
                setConfirm(true)
              }}
            >
              <RefreshCw /> New playback key
            </Button>
          </div>
        )}
      </div>
      {!key ? (
        <div className="space-y-2 text-sm">
          <p className="text-muted-foreground">
            {s.public
              ? 'This stream is public, so the addresses above need no key. These carry the playback key, for when you make it private.'
              : 'Addresses for players and other servers (VRChat, VLC, OBS). They carry the playback key, which lets people watch but never stream.'}
          </p>
          <Button
            variant="outline"
            onClick={() => {
              reveal.mutate()
            }}
            disabled={reveal.isPending || !s.keys.playback}
          >
            <Eye /> Show outputs
          </Button>
        </div>
      ) : targets.length === 0 ? (
        <p className="text-sm text-muted-foreground">
          No protocol for players is switched on in MediaMTX.
        </p>
      ) : (
        <>
          <TargetList targets={targets} secret={key.secret} shown={shown} prefix="output" />
          <p className="text-xs text-muted-foreground">
            Not offered: a plain HLS address (VRChat on Quest needs one), because the key would end
            up in the web server&apos;s logs. People with an account can watch in the browser on the
            Watch page.
          </p>
        </>
      )}
      {confirm && (
        <div className="flex flex-wrap items-center gap-2 rounded-lg border border-destructive/40 p-3 text-sm">
          <span>
            A new playback key stops the old addresses at once: players using them are disconnected.
          </span>
          <Button
            variant="destructive"
            size="sm"
            onClick={() => {
              regenerate.mutate()
            }}
            disabled={regenerate.isPending}
          >
            Make a new playback key
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
        </div>
      )}
      {(reveal.error ?? regenerate.error) && (
        <p className="text-sm text-destructive" role="alert">
          {errorText(reveal.error ?? regenerate.error, 'The playback key could not be loaded.')}
        </p>
      )}
    </section>
  )
}

/** A public stream's watch link and keyless addresses: the ones to hand out. */
function PublicOutputs({ stream: s }: { stream: Stream }) {
  const link = watchLink(s, window.location.origin)
  const targets = publicTargets(s, window.location.origin)
  return (
    <section className="space-y-3 rounded-xl border bg-card p-4" aria-labelledby="public-title">
      <h2 id="public-title" className="font-medium">
        Watch
      </h2>
      <div className="space-y-1.5">
        <p className="text-sm">
          <span className="font-medium">Watch link</span>{' '}
          <span className="text-muted-foreground">
            — anyone can open it in a browser, no account.
          </span>
        </p>
        <div className="flex items-center gap-2">
          <a
            href={link}
            target="_blank"
            rel="noreferrer"
            className="min-w-0 flex-1 rounded-md border bg-background px-2 py-1 font-mono text-xs break-all underline-offset-4 hover:underline"
            data-testid="watch-link"
          >
            {link}
          </a>
          <CopyButton text={link} label="watch link" />
        </div>
      </div>
      <TargetList targets={targets} secret="" shown prefix="public" />
    </section>
  )
}
