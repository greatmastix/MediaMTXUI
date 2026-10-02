import { zodResolver } from '@hookform/resolvers/zod'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useNavigate } from '@tanstack/react-router'
import { KeyRound } from 'lucide-react'
import { useState } from 'react'
import { useForm } from 'react-hook-form'
import { z } from 'zod'

import {
  login,
  loginCode,
  loginPasskey,
  sessionQuery,
  setupQuery,
  type Session,
} from '@/api/sidecar'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { passkeyError, passkeysSupported, signWithPasskey } from '@/lib/webauthn'

// Signing in: a password (then, with an authenticator app set up, its code or a recovery code), or a passkey alone.

const schema = z.object({
  username: z.string().trim().min(1, 'Enter your username.'),
  password: z.string().min(1, 'Enter your password.'),
})

type Values = z.infer<typeof schema>

export function LoginPage({ redirectTo = '/' }: { redirectTo?: string }) {
  const queryClient = useQueryClient()
  const navigate = useNavigate()
  const setup = useQuery(setupQuery)
  const [ticket, setTicket] = useState<string | null>(null)
  const [code, setCode] = useState('')
  const form = useForm<Values>({
    resolver: zodResolver(schema),
    defaultValues: { username: '', password: '' },
  })
  const done = async (session: Session) => {
    queryClient.setQueryData(sessionQuery.queryKey, session)
    await navigate({ href: redirectTo })
  }
  const signIn = useMutation({
    mutationFn: login,
    onSuccess: async (res) => {
      if ('secondFactor' in res) {
        setTicket(res.ticket)
        setCode('')
        return
      }
      await done(res)
    },
  })
  const second = useMutation({
    mutationFn: () => loginCode({ ticket: ticket ?? '', code }),
    onSuccess: done,
    onError: (e) => {
      if (e instanceof Error && 'kind' in e && e.kind === 'ticket') setTicket(null) // start over with the password
    },
  })
  const passkey = useMutation({
    mutationFn: () => loginPasskey(signWithPasskey),
    onSuccess: done,
  })
  const { errors } = form.formState
  const offerPasskey = setup.data?.passkeys === true && passkeysSupported()
  const error = signIn.error ?? second.error ?? passkey.error

  return (
    <div className="grid min-h-dvh place-items-center p-6">
      <Card className="w-full max-w-sm">
        <CardHeader>
          <CardTitle>
            <h1 className="font-heading text-xl font-semibold">Sign in to MediaMTX UI</h1>
          </CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          {ticket ? (
            <form
              className="space-y-4"
              onSubmit={(e) => {
                e.preventDefault()
                second.mutate()
              }}
            >
              <div className="space-y-1.5">
                <Label htmlFor="code">Code from your authenticator app</Label>
                <Input
                  id="code"
                  inputMode="text"
                  autoComplete="one-time-code"
                  autoFocus
                  value={code}
                  onChange={(e) => {
                    setCode(e.target.value)
                  }}
                />
                <p className="text-xs text-muted-foreground">
                  Lost your phone? Enter one of your recovery codes instead.
                </p>
              </div>
              {error && (
                <Alert variant="destructive" role="alert">
                  <AlertDescription>{error.message}</AlertDescription>
                </Alert>
              )}
              <Button type="submit" className="w-full" disabled={second.isPending || !code.trim()}>
                {second.isPending ? 'Checking…' : 'Sign in'}
              </Button>
              <Button
                type="button"
                variant="ghost"
                className="w-full"
                onClick={() => {
                  setTicket(null)
                  second.reset()
                }}
              >
                Back
              </Button>
            </form>
          ) : (
            <>
              <form
                className="space-y-4"
                noValidate
                onSubmit={(e) => {
                  void form.handleSubmit((v) => {
                    passkey.reset()
                    signIn.mutate(v)
                  })(e)
                }}
              >
                <div className="space-y-1.5">
                  <Label htmlFor="username">Username</Label>
                  <Input
                    id="username"
                    autoComplete="username webauthn"
                    autoFocus
                    {...form.register('username')}
                  />
                  {errors.username && (
                    <p className="text-sm text-destructive">{errors.username.message}</p>
                  )}
                </div>
                <div className="space-y-1.5">
                  <Label htmlFor="password">Password</Label>
                  <Input
                    id="password"
                    type="password"
                    autoComplete="current-password"
                    {...form.register('password')}
                  />
                  {errors.password && (
                    <p className="text-sm text-destructive">{errors.password.message}</p>
                  )}
                </div>
                {error && (
                  <Alert variant="destructive" role="alert">
                    <AlertDescription>
                      {error === passkey.error ? passkeyError(error) : error.message}
                    </AlertDescription>
                  </Alert>
                )}
                <Button type="submit" className="w-full" disabled={signIn.isPending}>
                  {signIn.isPending ? 'Signing in…' : 'Sign in'}
                </Button>
              </form>
              {offerPasskey && (
                <Button
                  variant="outline"
                  className="w-full"
                  disabled={passkey.isPending}
                  onClick={() => {
                    signIn.reset()
                    passkey.mutate()
                  }}
                >
                  <KeyRound /> Sign in with a passkey
                </Button>
              )}
            </>
          )}
        </CardContent>
      </Card>
    </div>
  )
}
