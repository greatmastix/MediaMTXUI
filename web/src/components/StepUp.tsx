import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { KeyRound, ShieldCheck } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'

import { accountQuery, stepUpPasskey, stepUpWith } from '@/api/account'
import { setStepUpHandler } from '@/api/client'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { passkeyError, passkeysSupported, signWithPasskey } from '@/lib/webauthn'

// Step-up: an admin-level change made more than an hour after the session last proved who it is
// stops at "Confirm it's you": the password, a code from the authenticator app, or a passkey. Then the change goes
// through by itself (the API client retries it). Requests that hit this together all wait for the one confirmation.

export function StepUpGate() {
  const queryClient = useQueryClient()
  const dialog = useRef<HTMLDialogElement>(null)
  const waiting = useRef<((ok: boolean) => void)[]>([])
  const [open, setOpen] = useState(false)
  const [password, setPassword] = useState('')
  const [code, setCode] = useState('')
  const account = useQuery({ ...accountQuery, enabled: open })

  useEffect(() => {
    setStepUpHandler(
      () =>
        new Promise<boolean>((resolve) => {
          waiting.current.push(resolve)
          setOpen(true)
        }),
    )
    return () => {
      setStepUpHandler(null)
    }
  }, [])
  useEffect(() => {
    const d = dialog.current
    if (!d) return
    if (open && !d.open) d.showModal()
    if (!open && d.open) d.close()
  }, [open])

  const finish = (ok: boolean) => {
    const resolvers = waiting.current
    waiting.current = []
    setOpen(false)
    setPassword('')
    setCode('')
    for (const r of resolvers) r(ok)
    if (ok) void queryClient.invalidateQueries({ queryKey: accountQuery.queryKey })
  }
  const confirm = useMutation({
    mutationFn: () => stepUpWith(code.trim() ? { code } : { password }),
    onSuccess: () => {
      finish(true)
    },
  })
  const passkey = useMutation({
    mutationFn: () => stepUpPasskey(signWithPasskey),
    onSuccess: () => {
      finish(true)
    },
  })
  const a = account.data
  const error = confirm.error ?? passkey.error

  return (
    <dialog
      ref={dialog}
      aria-labelledby="stepup-title"
      className="m-auto w-full max-w-sm rounded-xl border bg-card p-0 text-card-foreground backdrop:bg-black/60"
      onCancel={(e) => {
        e.preventDefault()
        finish(false)
      }}
    >
      <form
        className="space-y-4 p-5"
        onSubmit={(e) => {
          e.preventDefault()
          passkey.reset()
          confirm.mutate()
        }}
      >
        <div className="space-y-1">
          <h2
            id="stepup-title"
            className="flex items-center gap-2 font-heading text-lg font-semibold"
          >
            <ShieldCheck className="size-5 text-signal" aria-hidden /> Confirm it&apos;s you
          </h2>
          <p className="text-sm text-muted-foreground">
            This change needs you to confirm who you are (once an hour). You can switch this off on
            your Account page.
          </p>
        </div>
        <div className="space-y-1.5">
          <Label htmlFor="stepup-password">Password</Label>
          <Input
            id="stepup-password"
            type="password"
            autoComplete="current-password"
            autoFocus
            value={password}
            onChange={(e) => {
              setPassword(e.target.value)
            }}
          />
        </div>
        {a?.totp && (
          <div className="space-y-1.5">
            <Label htmlFor="stepup-code">Or a code from your authenticator app</Label>
            <Input
              id="stepup-code"
              autoComplete="one-time-code"
              value={code}
              onChange={(e) => {
                setCode(e.target.value)
              }}
            />
          </div>
        )}
        {error && (
          <p role="alert" className="text-sm text-destructive">
            {error === passkey.error ? passkeyError(error) : error.message}
          </p>
        )}
        <div className="flex flex-wrap gap-2">
          <Button type="submit" disabled={confirm.isPending || (!password && !code.trim())}>
            Confirm
          </Button>
          {a?.passkeysAvailable && a.passkeys.length > 0 && passkeysSupported() && (
            <Button
              type="button"
              variant="outline"
              disabled={passkey.isPending}
              onClick={() => {
                confirm.reset()
                passkey.mutate()
              }}
            >
              <KeyRound /> Use a passkey
            </Button>
          )}
          <Button
            type="button"
            variant="ghost"
            onClick={() => {
              finish(false)
            }}
          >
            Cancel
          </Button>
        </div>
      </form>
    </dialog>
  )
}
