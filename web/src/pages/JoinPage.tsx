import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Link, useNavigate } from '@tanstack/react-router'
import { useState } from 'react'

import { join, sessionQuery } from '@/api/sidecar'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'

// Where an invited person joins: the code an admin gave them (typed, never in the address) and a password of their
// own. They are signed in straight away.

export function JoinPage() {
  const queryClient = useQueryClient()
  const navigate = useNavigate()
  const [code, setCode] = useState('')
  const [password, setPassword] = useState('')
  const [again, setAgain] = useState('')
  const [mismatch, setMismatch] = useState(false)
  const submit = useMutation({
    mutationFn: join,
    onSuccess: async (session) => {
      queryClient.setQueryData(sessionQuery.queryKey, session)
      await navigate({ to: '/' })
    },
  })
  return (
    <div className="grid min-h-dvh place-items-center p-6">
      <Card className="w-full max-w-sm">
        <CardHeader>
          <CardTitle>
            <h1 className="font-heading text-xl font-semibold">Join MediaMTX UI</h1>
          </CardTitle>
        </CardHeader>
        <CardContent>
          <form
            className="space-y-4"
            onSubmit={(e) => {
              e.preventDefault()
              if (password !== again) {
                setMismatch(true)
                return
              }
              setMismatch(false)
              submit.mutate({ code, password })
            }}
          >
            <p className="text-sm text-muted-foreground">
              Enter the join code you were given and choose your password.
            </p>
            <div className="space-y-1.5">
              <Label htmlFor="code">Join code</Label>
              <Input
                id="code"
                autoComplete="one-time-code"
                autoCapitalize="characters"
                spellCheck={false}
                placeholder="XXXX-XXXX-XXXX-XXXX"
                className="font-mono tracking-wider"
                required
                value={code}
                onChange={(e) => {
                  setCode(e.target.value)
                }}
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="password">New password</Label>
              <Input
                id="password"
                type="password"
                autoComplete="new-password"
                required
                value={password}
                onChange={(e) => {
                  setPassword(e.target.value)
                }}
              />
              <p className="text-xs text-muted-foreground">At least 12 characters.</p>
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="again">Password again</Label>
              <Input
                id="again"
                type="password"
                autoComplete="new-password"
                required
                value={again}
                onChange={(e) => {
                  setAgain(e.target.value)
                }}
              />
            </div>
            {mismatch && (
              <p className="text-sm text-destructive" role="alert">
                The two passwords differ.
              </p>
            )}
            {submit.isError && (
              <Alert variant="destructive" role="alert">
                <AlertDescription>{submit.error.message}</AlertDescription>
              </Alert>
            )}
            <Button type="submit" className="w-full" disabled={submit.isPending}>
              {submit.isPending ? 'Joining…' : 'Join'}
            </Button>
            <p className="text-center text-sm">
              <Link to="/login" className="underline underline-offset-4">
                I have an account
              </Link>
            </p>
          </form>
        </CardContent>
      </Card>
    </div>
  )
}
