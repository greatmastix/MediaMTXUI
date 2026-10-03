import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { UserPlus } from 'lucide-react'
import { Fragment, useState } from 'react'

import { ApiError } from '@/api/client'
import {
  deletePerson,
  invite,
  newJoinCode,
  peopleQuery,
  setRole,
  type Invitation,
  type Person,
  endSessions,
  personSessionsQuery,
  resetSecondFactor,
  setDisabled,
} from '@/api/people'
import { streamsQuery, updateStream } from '@/api/streams'
import { sessionQuery } from '@/api/sidecar'
import { fieldClass } from '@/components/config/SettingsForm'
import { CopyButton } from '@/components/CopyButton'
import { StatusPill } from '@/components/StatusPill'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { formatTime } from '@/lib/format'
import { atLeast } from '@/lib/roles'

// People (admins): who can sign in, and invitations. An invited person gets a one-time join code from you and sets
// their own password at /join; the code works for 72 hours.

const roles: { value: Person['role']; label: string }[] = [
  { value: 'streamer', label: 'Streamer: their own streams only' },
  { value: 'viewer', label: 'Viewer: watches everything, changes nothing' },
  { value: 'operator', label: 'Operator: also manages every stream and disconnects encoders' },
  { value: 'admin', label: 'Admin: everything' },
]

function errorText(e: unknown, fallback: string) {
  return e instanceof ApiError ? e.message : fallback
}

export function PeoplePage() {
  const { data: session } = useQuery(sessionQuery)
  const admin = atLeast(session?.user.role ?? 'viewer', 'admin')
  const people = useQuery({ ...peopleQuery, enabled: admin })
  const [inviting, setInviting] = useState(false)
  const [code, setCode] = useState<Invitation | null>(null)
  if (!admin) {
    return (
      <div className="space-y-2">
        <h1 className="font-heading text-2xl font-semibold">People</h1>
        <p className="text-sm text-muted-foreground">People are managed by admins.</p>
      </div>
    )
  }
  return (
    <div className="max-w-5xl space-y-4">
      <div className="flex flex-wrap items-end justify-between gap-3">
        <div className="max-w-3xl space-y-1">
          <h1 className="font-heading text-2xl font-semibold">People</h1>
          <p className="text-sm text-muted-foreground">
            Who can sign in to this UI. Invite someone, pass them the join code, and they choose
            their own password. Streamers only ever see the streams they own.
          </p>
        </div>
        {!inviting && (
          <Button
            onClick={() => {
              setInviting(true)
              setCode(null)
            }}
          >
            <UserPlus /> Invite
          </Button>
        )}
      </div>
      {inviting && (
        <InviteForm
          onDone={(inv) => {
            setInviting(false)
            setCode(inv)
          }}
          onCancel={() => {
            setInviting(false)
          }}
        />
      )}
      {code && (
        <JoinCodePanel
          invitation={code}
          onClose={() => {
            setCode(null)
          }}
        />
      )}
      {!people.data ? (
        <Skeleton className="h-32 w-full" />
      ) : (
        <PeopleTable people={people.data} onCode={setCode} />
      )}
    </div>
  )
}

function InviteForm({
  onDone,
  onCancel,
}: {
  onDone: (inv: Invitation) => void
  onCancel: () => void
}) {
  const queryClient = useQueryClient()
  const [username, setUsername] = useState('')
  const [role, setRole] = useState<Person['role']>('streamer')
  const create = useMutation({
    mutationFn: invite,
    onSuccess: async (inv) => {
      await queryClient.invalidateQueries({ queryKey: peopleQuery.queryKey })
      onDone(inv)
    },
  })
  return (
    <form
      className="max-w-3xl space-y-3 rounded-xl border bg-card p-4"
      aria-label="Invite"
      onSubmit={(e) => {
        e.preventDefault()
        create.mutate({ username: username.trim(), role })
      }}
    >
      <div className="grid gap-3 sm:grid-cols-2">
        <label className="space-y-1 text-sm">
          <span className="font-medium">Username</span>
          <input
            className={fieldClass}
            value={username}
            autoComplete="off"
            required
            onChange={(e) => {
              setUsername(e.target.value)
            }}
          />
        </label>
        <label className="space-y-1 text-sm">
          <span className="font-medium">Role</span>
          <select
            className={fieldClass}
            value={role}
            onChange={(e) => {
              setRole(e.target.value as Person['role'])
            }}
          >
            {roles.map((r) => (
              <option key={r.value} value={r.value}>
                {r.label}
              </option>
            ))}
          </select>
        </label>
      </div>
      {create.error && (
        <Alert variant="destructive" role="alert">
          <AlertDescription>{errorText(create.error, 'The invitation failed.')}</AlertDescription>
        </Alert>
      )}
      <div className="flex gap-2">
        <Button type="submit" disabled={create.isPending || !username.trim()}>
          Invite
        </Button>
        <Button type="button" variant="outline" onClick={onCancel}>
          Cancel
        </Button>
      </div>
    </form>
  )
}

/** The one moment the join code is visible. */
function JoinCodePanel({
  invitation: inv,
  onClose,
}: {
  invitation: Invitation
  onClose: () => void
}) {
  const joinURL = `${window.location.origin}/join`
  return (
    <div
      className="max-w-3xl space-y-3 rounded-xl border border-signal/50 bg-card p-4"
      data-testid="join-code-panel"
    >
      <p className="font-medium">
        Give {inv.user.username} this join code. It is shown only now and works once, until{' '}
        {formatTime(inv.expires)}.
      </p>
      <div className="flex items-center gap-2">
        <code
          className="rounded-lg border bg-background px-3 py-2 font-mono text-lg tracking-wider select-all"
          data-testid="join-code"
        >
          {inv.joinCode}
        </code>
        <CopyButton text={inv.joinCode} label="join code" />
      </div>
      <p className="text-sm text-muted-foreground">
        They open <span className="font-mono">{joinURL}</span>, enter the code and choose a
        password. Send the code another way than the address if you can (a chat and a call, say).
      </p>
      {!inv.user.pending && (
        <p className="text-sm" data-testid="reset-code-note">
          Until then the code signs in as {inv.user.username} for whoever has it, and this page does
          not show it again. If it may have gone astray, make another one: that ends this one.
        </p>
      )}
      <Button variant="outline" onClick={onClose}>
        Done
      </Button>
    </div>
  )
}

function PeopleTable({ people, onCode }: { people: Person[]; onCode: (inv: Invitation) => void }) {
  const queryClient = useQueryClient()
  const { data: session } = useQuery(sessionQuery)
  const [confirm, setConfirm] = useState<number | null>(null)
  // A reset code signs in as that person, so it is asked for once more, with what it does.
  const [confirmReset, setConfirmReset] = useState<number | null>(null)
  const [editing, setEditing] = useState<number | null>(null)
  const refresh = () => queryClient.invalidateQueries({ queryKey: peopleQuery.queryKey })
  const code = useMutation({
    mutationFn: newJoinCode,
    onSuccess: (inv) => {
      setConfirmReset(null)
      onCode(inv)
    },
  })
  const remove = useMutation({
    mutationFn: deletePerson,
    onSuccess: async () => {
      setConfirm(null)
      await refresh()
    },
  })
  return (
    <div className="space-y-2">
      {(code.error ?? remove.error) && (
        <Alert variant="destructive" role="alert">
          <AlertDescription>
            {errorText(code.error ?? remove.error, 'That did not work.')}
          </AlertDescription>
        </Alert>
      )}
      <div className="rounded-lg border">
        <Table aria-label="People">
          <TableHeader>
            <TableRow>
              <TableHead>Username</TableHead>
              <TableHead>Role</TableHead>
              <TableHead>State</TableHead>
              <TableHead className="hidden md:table-cell">Streams</TableHead>
              <TableHead className="text-right">Actions</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {people.map((p) => (
              <Fragment key={p.id}>
                <TableRow data-testid={`person-${p.username}`}>
                  <TableCell className="font-medium">{p.username}</TableCell>
                  <TableCell>{p.role}</TableCell>
                  <TableCell>
                    {p.pending ? (
                      <StatusPill tone="warning">
                        Invited
                        {p.joinExpires
                          ? `, code until ${formatTime(p.joinExpires)}`
                          : ', code expired'}
                      </StatusPill>
                    ) : p.disabled ? (
                      <StatusPill tone="neutral">Disabled</StatusPill>
                    ) : (
                      <StatusPill tone="good">Active</StatusPill>
                    )}
                    {(p.totp || p.passkeys > 0) && (
                      <span
                        className="ml-1 text-xs text-muted-foreground"
                        data-testid="second-factor"
                      >
                        {[
                          p.totp && 'app',
                          p.passkeys > 0 &&
                            `${String(p.passkeys)} passkey${p.passkeys === 1 ? '' : 's'}`,
                        ]
                          .filter(Boolean)
                          .join(', ')}
                      </span>
                    )}
                  </TableCell>
                  <TableCell className="hidden md:table-cell">
                    {p.streams.length === 0
                      ? '–'
                      : p.streams.map((st, i) => (
                          <span key={st.id}>
                            {i > 0 && ', '}
                            <Link
                              to="/streams/$id"
                              params={{ id: String(st.id) }}
                              className="font-mono text-xs underline"
                            >
                              {st.name}
                            </Link>
                          </span>
                        ))}
                  </TableCell>
                  <TableCell className="space-x-2 text-right whitespace-nowrap">
                    <Button
                      size="sm"
                      variant="outline"
                      aria-expanded={editing === p.id}
                      onClick={() => {
                        setEditing(editing === p.id ? null : p.id)
                      }}
                    >
                      Edit
                    </Button>
                    {p.pending ? (
                      <Button
                        size="sm"
                        variant="outline"
                        onClick={() => {
                          code.mutate(p.id)
                        }}
                      >
                        New join code
                      </Button>
                    ) : confirmReset === p.id ? (
                      <>
                        <Button
                          size="sm"
                          variant="destructive"
                          disabled={code.isPending}
                          onClick={() => {
                            code.mutate(p.id)
                          }}
                        >
                          Make a reset code for {p.username}
                        </Button>
                        <Button
                          size="sm"
                          variant="outline"
                          onClick={() => {
                            setConfirmReset(null)
                          }}
                        >
                          Cancel
                        </Button>
                      </>
                    ) : (
                      <Button
                        size="sm"
                        variant="outline"
                        onClick={() => {
                          setConfirmReset(p.id)
                        }}
                      >
                        Reset password
                      </Button>
                    )}
                    {p.id !== session?.user.id &&
                      (confirm === p.id ? (
                        <>
                          <Button
                            size="sm"
                            variant="destructive"
                            onClick={() => {
                              remove.mutate(p.id)
                            }}
                          >
                            Delete {p.username}
                          </Button>
                          <Button
                            size="sm"
                            variant="outline"
                            onClick={() => {
                              setConfirm(null)
                            }}
                          >
                            Cancel
                          </Button>
                        </>
                      ) : (
                        <Button
                          size="sm"
                          variant="outline"
                          onClick={() => {
                            setConfirm(p.id)
                          }}
                        >
                          Delete
                        </Button>
                      ))}
                  </TableCell>
                </TableRow>
                {confirmReset === p.id && (
                  <TableRow>
                    <TableCell colSpan={5} className="whitespace-normal">
                      <p role="note" className="text-sm">
                        A reset code lets whoever has it choose a new password and sign in as{' '}
                        {p.username} straight away, without their authenticator app or passkey,
                        until it is used or expires. {p.username}&apos;s sessions end when it is
                        used. A new code replaces any earlier one.
                      </p>
                    </TableCell>
                  </TableRow>
                )}
                {editing === p.id && (
                  <TableRow>
                    <TableCell colSpan={5}>
                      <EditPerson
                        person={p}
                        self={p.id === session?.user.id}
                        onDone={() => {
                          setEditing(null)
                        }}
                      />
                      {!p.pending && <PersonAccess person={p} self={p.id === session?.user.id} />}
                    </TableCell>
                  </TableRow>
                )}
              </Fragment>
            ))}
          </TableBody>
        </Table>
      </div>
    </div>
  )
}

/** A person's role and the streams they own. */
function EditPerson({
  person: p,
  self,
  onDone,
}: {
  person: Person
  self: boolean
  onDone: () => void
}) {
  const queryClient = useQueryClient()
  const streams = useQuery(streamsQuery)
  const [role, setRoleValue] = useState<Person['role']>(p.role)
  const owned = new Set(p.streams.map((s) => s.id))
  const [picked, setPicked] = useState<Set<number>>(owned)
  const save = useMutation({
    mutationFn: async () => {
      if (role !== p.role) await setRole(p.id, role)
      for (const s of streams.data ?? []) {
        const want = picked.has(s.id)
        if (want !== owned.has(s.id)) await updateStream(s.id, { ownerId: want ? p.id : 0 })
      }
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: peopleQuery.queryKey })
      await queryClient.invalidateQueries({ queryKey: streamsQuery.queryKey })
      onDone()
    },
  })
  return (
    <form
      className="space-y-3 py-2"
      aria-label={`Edit ${p.username}`}
      onSubmit={(e) => {
        e.preventDefault()
        save.mutate()
      }}
    >
      <label className="block max-w-sm space-y-1 text-sm">
        <span className="font-medium">Role</span>
        <select
          className={fieldClass}
          value={role}
          disabled={self}
          onChange={(e) => {
            setRoleValue(e.target.value as Person['role'])
          }}
        >
          {roles.map((r) => (
            <option key={r.value} value={r.value}>
              {r.label}
            </option>
          ))}
        </select>
        {self && (
          <span className="block text-xs text-muted-foreground">
            You cannot change your own role.
          </span>
        )}
      </label>
      <fieldset className="space-y-1">
        <legend className="mb-1 text-sm font-medium">Owns</legend>
        {streams.data?.length === 0 && (
          <p className="text-sm text-muted-foreground">No streams yet.</p>
        )}
        <div className="grid gap-1 sm:grid-cols-2 lg:grid-cols-3">
          {streams.data?.map((s) => (
            <label key={s.id} className="flex items-center gap-2 text-sm">
              <input
                type="checkbox"
                checked={picked.has(s.id)}
                onChange={(e) => {
                  const next = new Set(picked)
                  if (e.target.checked) next.add(s.id)
                  else next.delete(s.id)
                  setPicked(next)
                }}
              />
              <span className="font-mono text-xs">{s.name}</span>
              {s.owner && s.owner.id !== p.id && (
                <span className="text-xs text-muted-foreground">(now {s.owner.username})</span>
              )}
            </label>
          ))}
        </div>
      </fieldset>
      {save.error && (
        <p role="alert" className="text-sm text-destructive">
          {errorText(save.error, 'The changes were not all saved.')}
        </p>
      )}
      <div className="flex gap-2">
        <Button type="submit" size="sm" disabled={save.isPending}>
          Save
        </Button>
        <Button type="button" size="sm" variant="outline" onClick={onDone}>
          Cancel
        </Button>
      </div>
    </form>
  )
}

/** A person's sign-in: their sessions, signing them out, disabling the account, resetting a lost second factor. */
function PersonAccess({ person: p, self }: { person: Person; self: boolean }) {
  const queryClient = useQueryClient()
  const sessions = useQuery(personSessionsQuery(p.id))
  const refresh = () => {
    void queryClient.invalidateQueries({ queryKey: peopleQuery.queryKey })
    void queryClient.invalidateQueries({ queryKey: personSessionsQuery(p.id).queryKey })
  }
  const disable = useMutation({
    mutationFn: (d: boolean) => setDisabled(p.id, d),
    onSuccess: refresh,
  })
  const signOut = useMutation({ mutationFn: () => endSessions(p.id), onSuccess: refresh })
  const reset = useMutation({ mutationFn: () => resetSecondFactor(p.id), onSuccess: refresh })
  const n = sessions.data?.length ?? 0
  const error = disable.error ?? signOut.error ?? reset.error
  return (
    <div
      className="mt-3 space-y-2 border-t pt-3 text-sm"
      aria-label={`${p.username}'s sign-in`}
      role="group"
    >
      <p className="font-medium">Sign-in and access</p>
      <p className="text-muted-foreground">
        {sessions.data ? `Signed in ${String(n)} time${n === 1 ? '' : 's'}` : 'Sessions…'}
        {p.totp ? '; uses an authenticator app' : ''}
        {p.passkeys > 0 ? `; ${String(p.passkeys)} passkey${p.passkeys === 1 ? '' : 's'}` : ''}.
      </p>
      <div className="flex flex-wrap gap-2">
        {n > 0 && (
          <Button
            size="sm"
            variant="outline"
            disabled={signOut.isPending}
            onClick={() => {
              signOut.mutate()
            }}
          >
            {self ? 'Sign out everywhere else' : 'Sign out everywhere'}
          </Button>
        )}
        {!self && (
          <Button
            size="sm"
            variant="outline"
            disabled={disable.isPending}
            onClick={() => {
              disable.mutate(!p.disabled)
            }}
          >
            {p.disabled ? 'Enable the account' : 'Disable the account'}
          </Button>
        )}
        {(p.totp || p.passkeys > 0) && (
          <Button
            size="sm"
            variant="outline"
            disabled={reset.isPending}
            onClick={() => {
              reset.mutate()
            }}
          >
            Reset second factor
          </Button>
        )}
      </div>
      {reset.isSuccess && (
        <p role="status">
          Their authenticator app, recovery codes and passkeys are gone; they sign in with their
          password.
        </p>
      )}
      {error && (
        <p role="alert" className="text-destructive">
          {errorText(error, 'That did not work.')}
        </p>
      )}
    </div>
  )
}
