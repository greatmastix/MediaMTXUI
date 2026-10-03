import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  KeyRound,
  LogOut,
  MonitorSmartphone,
  ShieldCheck,
  Smartphone,
  UserRound,
} from 'lucide-react'
import { useState } from 'react'
import { encode } from 'uqr'

import {
  accountQuery,
  addPasskey,
  changePassword,
  endMySession,
  mySessionsQuery,
  newRecoveryCodes,
  removePasskey,
  renamePasskey,
  setStepUpOptOut,
  totpDisable,
  totpEnable,
  totpSetup,
  type Account,
} from '@/api/account'
import { ApiError } from '@/api/client'
import { fieldClass } from '@/components/config/SettingsForm'
import { CopyButton } from '@/components/CopyButton'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { useNow } from '@/hooks/useNow'
import { formatSince, formatTime } from '@/lib/format'
import { createPasskey, passkeyError, passkeysSupported } from '@/lib/webauthn'

// Your account, for every role: password, an authenticator app with recovery codes, passkeys, your
// sessions, and (admins) whether admin-level changes ask you to confirm it is you.

const errorText = (e: unknown, fallback: string) =>
  e instanceof ApiError || e instanceof Error ? e.message : fallback

export function AccountPage() {
  const account = useQuery(accountQuery)
  if (!account.data) return <Skeleton className="h-64 w-full max-w-3xl" />
  const a = account.data
  return (
    <div className="max-w-3xl space-y-6">
      <div className="space-y-1">
        <h1 className="flex items-center gap-2 font-heading text-2xl font-semibold">
          <UserRound className="size-6 text-signal" aria-hidden /> Your account
        </h1>
        <p className="text-sm text-muted-foreground">
          Signed in as <span className="font-medium text-foreground">{a.username}</span> ({a.role}).
        </p>
      </div>
      <Password />
      <Authenticator account={a} />
      <Passkeys account={a} />
      <Sessions />
      {a.role === 'admin' && <StepUpSetting account={a} />}
    </div>
  )
}

function Section({
  title,
  icon: Icon,
  children,
}: {
  title: string
  icon: typeof KeyRound
  children: React.ReactNode
}) {
  const id = `account-${title.toLowerCase().replace(/[^a-z]+/g, '-')}`
  return (
    <section className="space-y-3 rounded-xl border bg-card p-4" aria-labelledby={id}>
      <h2 id={id} className="flex items-center gap-2 font-medium">
        <Icon className="size-4 text-signal" aria-hidden /> {title}
      </h2>
      {children}
    </section>
  )
}

function Password() {
  const [current, setCurrent] = useState('')
  const [next, setNext] = useState('')
  const [again, setAgain] = useState('')
  const change = useMutation({
    mutationFn: () => changePassword(current, next),
    onSuccess: () => {
      setCurrent('')
      setNext('')
      setAgain('')
    },
  })
  const mismatch = again !== '' && next !== again
  return (
    <Section title="Password" icon={KeyRound}>
      <form
        className="grid gap-3 sm:grid-cols-3"
        onSubmit={(e) => {
          e.preventDefault()
          change.mutate()
        }}
      >
        {(
          [
            ['Current password', current, setCurrent, 'current-password'],
            ['New password', next, setNext, 'new-password'],
            ['New password again', again, setAgain, 'new-password'],
          ] as const
        ).map(([label, value, set, auto]) => (
          <label key={label} className="space-y-1 text-sm">
            <span className="font-medium">{label}</span>
            <input
              type="password"
              className={fieldClass}
              autoComplete={auto}
              value={value}
              onChange={(e) => {
                set(e.target.value)
              }}
            />
          </label>
        ))}
        <div className="flex flex-wrap items-center gap-3 sm:col-span-3">
          <Button type="submit" disabled={change.isPending || !current || !next || next !== again}>
            Change password
          </Button>
          {mismatch && <span className="text-sm text-destructive">The new passwords differ.</span>}
          {change.isSuccess && (
            <span role="status" className="text-sm">
              Changed. Your other sessions were signed out.
            </span>
          )}
          {change.error && (
            <span role="alert" className="text-sm text-destructive">
              {errorText(change.error, 'The password was not changed.')}
            </span>
          )}
        </div>
      </form>
    </Section>
  )
}

/** A QR code drawn as SVG squares (no image: the CSP allows none from elsewhere). */
function QR({ text }: { text: string }) {
  const { data, size } = encode(text, { border: 2 })
  return (
    <svg
      viewBox={`0 0 ${String(size)} ${String(size)}`}
      className="size-44 rounded-md bg-white"
      role="img"
      aria-label="QR code for your authenticator app"
      shapeRendering="crispEdges"
    >
      {data.flatMap((row, y) =>
        row.map((on, x) =>
          on ? (
            <rect key={`${String(x)}-${String(y)}`} x={x} y={y} width={1} height={1} fill="#000" />
          ) : null,
        ),
      )}
    </svg>
  )
}

function RecoveryCodes({ codes, onDone }: { codes: string[]; onDone: () => void }) {
  const text = codes.join('\n')
  return (
    <div className="space-y-2 rounded-lg border border-signal/40 p-3" data-testid="recovery-codes">
      <p className="text-sm">
        <span className="font-medium">Your recovery codes.</span>{' '}
        <span className="text-muted-foreground">
          Each signs you in once instead of an app code, if you lose your phone. They are shown only
          now: keep them somewhere safe.
        </span>
      </p>
      <ul className="grid grid-cols-2 gap-1 font-mono text-sm sm:grid-cols-5">
        {codes.map((c) => (
          <li key={c}>{c}</li>
        ))}
      </ul>
      <div className="flex flex-wrap gap-2">
        <CopyButton text={text} label="recovery codes" />
        <a
          className="text-sm underline-offset-4 hover:underline"
          download="mediamtx-ui-recovery-codes.txt"
          href={`data:text/plain;charset=utf-8,${encodeURIComponent(text + '\n')}`}
        >
          Download
        </a>
        <Button size="sm" onClick={onDone}>
          I have saved them
        </Button>
      </div>
    </div>
  )
}

/** Asks for the password before a change that needs it. */
function WithPassword({
  label,
  run,
  onCancel,
}: {
  label: string
  run: (password: string) => Promise<unknown>
  onCancel: () => void
}) {
  const [password, setPassword] = useState('')
  const go = useMutation({ mutationFn: () => run(password) })
  return (
    <form
      className="flex flex-wrap items-end gap-2"
      onSubmit={(e) => {
        e.preventDefault()
        go.mutate()
      }}
    >
      <label className="space-y-1 text-sm">
        <span className="font-medium">Your password</span>
        <input
          type="password"
          autoComplete="current-password"
          className={fieldClass}
          value={password}
          autoFocus
          onChange={(e) => {
            setPassword(e.target.value)
          }}
        />
      </label>
      <Button type="submit" size="sm" disabled={go.isPending || !password}>
        {label}
      </Button>
      <Button type="button" size="sm" variant="ghost" onClick={onCancel}>
        Cancel
      </Button>
      {go.error && (
        <p role="alert" className="w-full text-sm text-destructive">
          {errorText(go.error, 'That did not work.')}
        </p>
      )}
    </form>
  )
}

function Authenticator({ account: a }: { account: Account }) {
  const queryClient = useQueryClient()
  const refresh = () => {
    void queryClient.invalidateQueries({ queryKey: accountQuery.queryKey })
  }
  const [code, setCode] = useState('')
  const [codes, setCodes] = useState<string[] | null>(null)
  const [asking, setAsking] = useState<'off' | 'codes' | null>(null)
  const setup = useMutation({ mutationFn: totpSetup })
  const [askSetup, setAskSetup] = useState(false)
  const enable = useMutation({
    mutationFn: () => totpEnable(code),
    onSuccess: (res) => {
      setCodes(res.recoveryCodes)
      setup.reset()
      setCode('')
      refresh()
    },
  })
  return (
    <Section title="Authenticator app" icon={Smartphone}>
      <p className="text-sm text-muted-foreground">
        Optional: after your password, a six-digit code from an app on your phone (Google
        Authenticator, Microsoft Authenticator, 1Password, …) is asked too.
      </p>
      {codes && (
        <RecoveryCodes
          codes={codes}
          onDone={() => {
            setCodes(null)
          }}
        />
      )}
      {a.totp ? (
        <>
          <p className="text-sm" data-testid="totp-state">
            On. {a.recoveryCodesLeft} recovery code{a.recoveryCodesLeft === 1 ? '' : 's'} left.
          </p>
          {asking ? (
            <WithPassword
              label={asking === 'off' ? 'Switch off' : 'Make new codes'}
              onCancel={() => {
                setAsking(null)
              }}
              run={async (password) => {
                if (asking === 'off') {
                  await totpDisable(password)
                } else {
                  setCodes((await newRecoveryCodes(password)).recoveryCodes)
                }
                setAsking(null)
                refresh()
              }}
            />
          ) : (
            <div className="flex flex-wrap gap-2">
              <Button
                size="sm"
                variant="outline"
                onClick={() => {
                  setAsking('codes')
                }}
              >
                New recovery codes
              </Button>
              <Button
                size="sm"
                variant="outline"
                onClick={() => {
                  setAsking('off')
                }}
              >
                Switch off
              </Button>
            </div>
          )}
        </>
      ) : setup.data ? (
        <div className="space-y-3">
          <div className="flex flex-wrap items-start gap-4">
            <QR text={setup.data.uri} />
            <div className="min-w-0 flex-1 space-y-2 text-sm">
              <p>Scan this with your authenticator app, or type in the key:</p>
              <p className="flex items-center gap-2">
                <code
                  className="rounded-md border bg-background px-2 py-1 font-mono text-xs break-all"
                  data-testid="totp-secret"
                >
                  {setup.data.secret.replace(/(.{4})/g, '$1 ').trim()}
                </code>
                <CopyButton text={setup.data.secret} label="key" />
              </p>
              <form
                className="flex flex-wrap items-end gap-2"
                onSubmit={(e) => {
                  e.preventDefault()
                  enable.mutate()
                }}
              >
                <label className="space-y-1">
                  <span className="font-medium">The code it shows</span>
                  <input
                    className={fieldClass}
                    inputMode="numeric"
                    autoComplete="one-time-code"
                    value={code}
                    onChange={(e) => {
                      setCode(e.target.value)
                    }}
                  />
                </label>
                <Button type="submit" disabled={enable.isPending || code.trim().length < 6}>
                  Switch on
                </Button>
                <Button
                  type="button"
                  variant="ghost"
                  onClick={() => {
                    setup.reset()
                  }}
                >
                  Cancel
                </Button>
              </form>
              {enable.error && (
                <p role="alert" className="text-destructive">
                  {errorText(enable.error, 'The app was not switched on.')}
                </p>
              )}
            </div>
          </div>
        </div>
      ) : askSetup ? (
        <WithPassword
          label="Continue"
          run={async (password) => {
            await setup.mutateAsync(password)
            setAskSetup(false)
          }}
          onCancel={() => {
            setAskSetup(false)
          }}
        />
      ) : (
        <Button
          size="sm"
          onClick={() => {
            setAskSetup(true)
          }}
        >
          Set up an authenticator app
        </Button>
      )}
    </Section>
  )
}

function Passkeys({ account: a }: { account: Account }) {
  const queryClient = useQueryClient()
  const now = useNow(60_000)
  const [name, setName] = useState('')
  const [renaming, setRenaming] = useState<{ id: number; name: string } | null>(null)
  const saved = (next: Account) => {
    queryClient.setQueryData(accountQuery.queryKey, next)
  }
  const [password, setPassword] = useState('')
  const add = useMutation({
    mutationFn: () => addPasskey(name.trim(), password, createPasskey),
    onSuccess: (next) => {
      saved(next)
      setName('')
      setPassword('')
    },
  })
  const rename = useMutation({
    mutationFn: (r: { id: number; name: string }) => renamePasskey(r.id, r.name),
    onSuccess: (next) => {
      saved(next)
      setRenaming(null)
    },
  })
  const remove = useMutation({ mutationFn: removePasskey, onSuccess: saved })
  const usable = a.passkeysAvailable && passkeysSupported()
  return (
    <Section title="Passkeys" icon={KeyRound}>
      <p className="text-sm text-muted-foreground">
        Sign in with your fingerprint, face or device PIN instead of your password (and confirm it
        is you the same way). Your password stays, so removing a passkey never locks you out.
      </p>
      {!usable && (
        <p className="text-sm text-muted-foreground">
          {a.passkeysAvailable
            ? 'This browser cannot use passkeys here.'
            : 'Passkeys need the UI on an HTTPS address, which this server does not have.'}
        </p>
      )}
      {a.passkeys.length > 0 && (
        <ul className="divide-y rounded-lg border" aria-label="Your passkeys">
          {a.passkeys.map((k) => (
            <li key={k.id} className="flex flex-wrap items-center justify-between gap-2 p-3">
              {renaming?.id === k.id ? (
                <form
                  className="flex flex-wrap items-center gap-2"
                  onSubmit={(e) => {
                    e.preventDefault()
                    rename.mutate(renaming)
                  }}
                >
                  <input
                    aria-label="Passkey name"
                    className={fieldClass}
                    value={renaming.name}
                    autoFocus
                    onChange={(e) => {
                      setRenaming({ id: k.id, name: e.target.value })
                    }}
                  />
                  <Button type="submit" size="sm" disabled={rename.isPending}>
                    Save
                  </Button>
                  <Button
                    type="button"
                    size="sm"
                    variant="ghost"
                    onClick={() => {
                      setRenaming(null)
                    }}
                  >
                    Cancel
                  </Button>
                </form>
              ) : (
                <div className="min-w-0">
                  <p className="font-medium">{k.name}</p>
                  <p className="text-xs text-muted-foreground">
                    Added {formatTime(k.createdAt)}
                    {k.lastUsedAt
                      ? `, last used ${formatSince(k.lastUsedAt, now)}`
                      : ', not used yet'}
                  </p>
                </div>
              )}
              {renaming?.id !== k.id && (
                <div className="flex gap-2">
                  <Button
                    size="sm"
                    variant="outline"
                    onClick={() => {
                      setRenaming({ id: k.id, name: k.name })
                    }}
                  >
                    Rename
                  </Button>
                  <Button
                    size="sm"
                    variant="outline"
                    disabled={remove.isPending}
                    onClick={() => {
                      remove.mutate(k.id)
                    }}
                  >
                    Remove
                  </Button>
                </div>
              )}
            </li>
          ))}
        </ul>
      )}
      {usable && (
        <form
          className="flex flex-wrap items-end gap-2"
          onSubmit={(e) => {
            e.preventDefault()
            add.mutate()
          }}
        >
          <label className="space-y-1 text-sm">
            <span className="font-medium">Name for the new passkey</span>
            <input
              className={fieldClass}
              placeholder="Laptop, Phone, YubiKey…"
              maxLength={60}
              value={name}
              onChange={(e) => {
                setName(e.target.value)
              }}
            />
          </label>
          <label className="space-y-1 text-sm">
            <span className="font-medium">Your password</span>
            <input
              type="password"
              autoComplete="current-password"
              className={fieldClass}
              value={password}
              onChange={(e) => {
                setPassword(e.target.value)
              }}
            />
          </label>
          <Button type="submit" size="sm" disabled={add.isPending || !password}>
            <KeyRound /> Add a passkey
          </Button>
        </form>
      )}
      {(add.error ?? rename.error ?? remove.error) && (
        <p role="alert" className="text-sm text-destructive">
          {add.error
            ? passkeyError(add.error)
            : errorText(rename.error ?? remove.error, 'That did not work.')}
        </p>
      )}
    </Section>
  )
}

/** A browser and system from a user agent, roughly. */
function device(ua: string) {
  const browser = ua.includes('Edg/')
    ? 'Edge'
    : ua.includes('Firefox/')
      ? 'Firefox'
      : ua.includes('Chrome/')
        ? 'Chrome'
        : ua.includes('Safari/')
          ? 'Safari'
          : 'A browser'
  const system = ua.includes('Windows')
    ? 'Windows'
    : ua.includes('Android')
      ? 'Android'
      : /iPhone|iPad/.test(ua)
        ? 'iOS'
        : ua.includes('Mac OS X')
          ? 'macOS'
          : ua.includes('Linux')
            ? 'Linux'
            : ''
  return system ? `${browser} on ${system}` : browser
}

function Sessions() {
  const queryClient = useQueryClient()
  const now = useNow(60_000)
  const sessions = useQuery(mySessionsQuery)
  const end = useMutation({
    mutationFn: endMySession,
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: mySessionsQuery.queryKey })
    },
  })
  const list = sessions.data ?? []
  return (
    <Section title="Where you are signed in" icon={MonitorSmartphone}>
      <ul className="divide-y rounded-lg border" aria-label="Your sessions">
        {list.map((s) => (
          <li
            key={s.handle}
            className="flex flex-wrap items-center justify-between gap-2 p-3 text-sm"
          >
            <div className="min-w-0">
              <p className="font-medium">
                {device(s.userAgent)} {s.current && <span className="text-signal">· this one</span>}
              </p>
              <p className="text-xs text-muted-foreground">
                {s.ip} · signed in {formatSince(s.createdAt, now)} with{' '}
                {s.method.replace('+', ' and ')} · active {formatSince(s.lastSeenAt, now)}
              </p>
            </div>
            {!s.current && (
              <Button
                size="sm"
                variant="outline"
                disabled={end.isPending}
                onClick={() => {
                  end.mutate(s.handle)
                }}
              >
                <LogOut /> Sign out
              </Button>
            )}
          </li>
        ))}
      </ul>
      {list.length > 1 && (
        <Button
          size="sm"
          variant="outline"
          disabled={end.isPending}
          onClick={() => {
            end.mutate('others')
          }}
        >
          Sign out everywhere else
        </Button>
      )}
    </Section>
  )
}

function StepUpSetting({ account: a }: { account: Account }) {
  const queryClient = useQueryClient()
  const set = useMutation({
    mutationFn: setStepUpOptOut,
    onSuccess: (next) => {
      queryClient.setQueryData(accountQuery.queryKey, next)
    },
  })
  return (
    <Section title="Confirming it's you" icon={ShieldCheck}>
      <label className="flex items-start gap-2 text-sm">
        <input
          type="checkbox"
          className="mt-1"
          checked={!a.stepUpOptOut}
          disabled={set.isPending}
          onChange={(e) => {
            set.mutate(!e.target.checked)
          }}
        />
        <span>
          <span className="font-medium">Ask me before admin-level changes</span>
          <span className="block text-xs text-muted-foreground">
            People and roles, credentials, the YAML editor and restoring versions, exposure,
            backups: your password, an app code or a passkey, at most once an hour. If someone got
            hold of your signed-in browser, this stops them there.
          </span>
        </span>
      </label>
      {set.error && (
        <p role="alert" className="text-sm text-destructive">
          {errorText(set.error, 'That did not work.')}
        </p>
      )}
    </Section>
  )
}
