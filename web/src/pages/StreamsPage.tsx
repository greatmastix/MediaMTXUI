import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link, useNavigate } from '@tanstack/react-router'
import { Plus, Radio } from 'lucide-react'
import { useState } from 'react'

import { ApiError } from '@/api/client'
import { peopleQuery } from '@/api/people'
import { sessionQuery } from '@/api/sidecar'
import { createStream, streamsQuery, type Stream } from '@/api/streams'
import { fieldClass } from '@/components/config/SettingsForm'
import { StatusPill } from '@/components/StatusPill'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { formatBitrate } from '@/lib/format'
import { atLeast } from '@/lib/roles'
import { useStreamLive } from '@/live/useStreamLive'

// The streams, as the people who stream see them: one card each, live or not, with viewers and bitrate. Admins create
// streams here and pick who owns them.

export function StreamsPage() {
  const { data: session } = useQuery(sessionQuery)
  const admin = atLeast(session?.user.role ?? 'streamer', 'admin')
  const streams = useQuery(streamsQuery)
  const [creating, setCreating] = useState(false)
  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-end justify-between gap-3">
        <div className="max-w-3xl space-y-1">
          <h1 className="font-heading text-2xl font-semibold">Streams</h1>
          <p className="text-sm text-muted-foreground">
            {admin
              ? 'Each stream has its own page with its server address and stream key, a preview, and its viewers. Give one to a streamer by making them its owner.'
              : 'Your streams. Open one for its server address and stream key, a preview and how it is doing.'}
          </p>
        </div>
        {admin && !creating && (
          <Button
            onClick={() => {
              setCreating(true)
            }}
          >
            <Plus /> New stream
          </Button>
        )}
      </div>
      {creating && (
        <NewStream
          onClose={() => {
            setCreating(false)
          }}
        />
      )}
      {!streams.data ? (
        <Skeleton className="h-32 w-full max-w-4xl" />
      ) : streams.data.length === 0 ? (
        <p className="max-w-4xl rounded-lg border border-dashed p-8 text-center text-sm text-muted-foreground">
          {admin
            ? 'No streams yet. Create one for each channel or person that streams here.'
            : 'You have no streams yet. Ask an admin to give you one.'}
        </p>
      ) : (
        <ul className="grid max-w-5xl gap-3 sm:grid-cols-2 lg:grid-cols-3" aria-label="Streams">
          {streams.data.map((s) => (
            <StreamCard key={s.id} stream={s} />
          ))}
        </ul>
      )}
    </div>
  )
}

function StreamCard({ stream: s }: { stream: Stream }) {
  const { online, viewers, inBps } = useStreamLive(s.name)
  return (
    <li>
      <Link
        to="/streams/$id"
        params={{ id: String(s.id) }}
        className="block space-y-2 rounded-xl border bg-card p-4 hover:border-signal/60"
        data-testid={`stream-${s.name}`}
      >
        <div className="flex items-start justify-between gap-2">
          <div className="min-w-0">
            <p className="truncate font-medium">{s.title}</p>
            <p className="truncate font-mono text-xs text-muted-foreground">{s.name}</p>
          </div>
          {online ? (
            <StatusPill tone="good">
              <Radio className="size-3 text-signal" aria-hidden /> Live
            </StatusPill>
          ) : (
            <StatusPill tone="neutral">Offline</StatusPill>
          )}
        </div>
        <p className="text-xs text-muted-foreground tabular-nums">
          {online
            ? `${String(viewers)} watching · ${inBps !== undefined ? formatBitrate(inBps) : '…'}`
            : 'Not streaming'}
          {s.owner ? ` · ${s.owner.username}` : ''}
        </p>
      </Link>
    </li>
  )
}

function NewStream({ onClose }: { onClose: () => void }) {
  const queryClient = useQueryClient()
  const navigate = useNavigate()
  const people = useQuery(peopleQuery)
  const [name, setName] = useState('')
  const [title, setTitle] = useState('')
  const [owner, setOwner] = useState('')
  const create = useMutation({
    mutationFn: createStream,
    onSuccess: async (s) => {
      await queryClient.invalidateQueries({ queryKey: streamsQuery.queryKey })
      await navigate({ to: '/streams/$id', params: { id: String(s.id) } })
    },
  })
  return (
    <form
      className="max-w-3xl space-y-3 rounded-xl border bg-card p-4"
      aria-label="New stream"
      onSubmit={(e) => {
        e.preventDefault()
        create.mutate({
          name: name.trim(),
          title: title.trim(),
          ownerId: owner ? Number(owner) : null,
          target: '',
        })
      }}
    >
      <div className="grid gap-3 sm:grid-cols-2">
        <label className="space-y-1 text-sm">
          <span className="font-medium">Path</span>
          <input
            className={fieldClass}
            value={name}
            placeholder="live/alice"
            required
            onChange={(e) => {
              setName(e.target.value)
            }}
          />
          <span className="block text-xs text-muted-foreground">
            The stream&apos;s name in MediaMTX; part of every address.
          </span>
        </label>
        <label className="space-y-1 text-sm">
          <span className="font-medium">Title</span>
          <input
            className={fieldClass}
            value={title}
            placeholder="Alice live"
            onChange={(e) => {
              setTitle(e.target.value)
            }}
          />
        </label>
        <label className="space-y-1 text-sm">
          <span className="font-medium">Owner</span>
          <select
            className={fieldClass}
            value={owner}
            onChange={(e) => {
              setOwner(e.target.value)
            }}
          >
            <option value="">Nobody (admins and operators manage it)</option>
            {people.data?.map((p) => (
              <option key={p.id} value={p.id}>
                {p.username} ({p.role}
                {p.pending ? ', invited' : ''})
              </option>
            ))}
          </select>
          <span className="block text-xs text-muted-foreground">
            Invite a streamer on the <Link to="/people">People</Link> page first.
          </span>
        </label>
      </div>
      {create.error && (
        <Alert variant="destructive" role="alert">
          <AlertDescription>
            {create.error instanceof ApiError
              ? create.error.message
              : 'The stream was not created.'}
          </AlertDescription>
        </Alert>
      )}
      <div className="flex gap-2">
        <Button type="submit" disabled={create.isPending || !name.trim()}>
          Create stream
        </Button>
        <Button type="button" variant="outline" onClick={onClose}>
          Cancel
        </Button>
      </div>
    </form>
  )
}
