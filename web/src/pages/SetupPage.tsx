import { zodResolver } from '@hookform/resolvers/zod'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useNavigate } from '@tanstack/react-router'
import { Controller, useForm } from 'react-hook-form'
import { z } from 'zod'

import { changeSession, completeSetup, setupQuery } from '@/api/sidecar'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'

const schema = z
  .object({
    token: z.string().trim().min(1, 'Enter the setup token.'),
    username: z
      .string()
      .regex(
        /^[A-Za-z0-9][A-Za-z0-9._-]{1,31}$/,
        '2 to 32 letters, digits, dots, underscores or hyphens, starting with a letter or digit.',
      ),
    password: z.string().min(12, 'At least 12 characters.'),
    confirm: z.string(),
    rtsp: z.boolean(),
    rtmp: z.boolean(),
    srt: z.boolean(),
  })
  .refine((v) => v.password === v.confirm, { message: 'The passwords differ.', path: ['confirm'] })

type Values = z.infer<typeof schema>

const protocols = [
  { name: 'rtsp', label: 'RTSP', hint: 'cameras, OBS, ffmpeg; port 8554/tcp' },
  { name: 'rtmp', label: 'RTMP', hint: 'OBS and streaming software; port 1935/tcp' },
  { name: 'srt', label: 'SRT', hint: 'contribution over lossy links; port 8890/udp' },
] as const

export function SetupPage() {
  const queryClient = useQueryClient()
  const navigate = useNavigate()
  const form = useForm<Values>({
    resolver: zodResolver(schema),
    defaultValues: {
      token: '',
      username: 'admin',
      password: '',
      confirm: '',
      rtsp: false,
      rtmp: false,
      srt: false,
    },
  })
  const setup = useMutation({
    mutationFn: (v: Values) =>
      completeSetup({
        token: v.token,
        username: v.username,
        password: v.password,
        ingest: { rtsp: v.rtsp, rtmp: v.rtmp, srt: v.srt },
      }),
    onSuccess: async (session) => {
      changeSession(queryClient, session)
      queryClient.setQueryData(setupQuery.queryKey, { required: false })
      await navigate({ to: '/' })
    },
  })
  const { errors } = form.formState

  return (
    <div className="grid min-h-dvh place-items-center p-6">
      <Card className="w-full max-w-md">
        <CardHeader>
          <CardTitle>
            <h1 className="font-heading text-xl font-semibold">Set up MediaMTX UI</h1>
          </CardTitle>
          <CardDescription>
            Enter the setup token from the sidecar&apos;s log, then create the first admin.
          </CardDescription>
        </CardHeader>
        <CardContent>
          <form
            className="space-y-4"
            noValidate
            onSubmit={(e) => {
              void form.handleSubmit((v) => {
                setup.mutate(v)
              })(e)
            }}
          >
            <Field id="token" label="Setup token" error={errors.token?.message}>
              <Input id="token" autoComplete="off" spellCheck={false} {...form.register('token')} />
            </Field>
            <Field id="username" label="Admin username" error={errors.username?.message}>
              <Input id="username" autoComplete="username" {...form.register('username')} />
            </Field>
            <Field id="password" label="Password" error={errors.password?.message}>
              <Input
                id="password"
                type="password"
                autoComplete="new-password"
                {...form.register('password')}
              />
            </Field>
            <Field id="confirm" label="Password again" error={errors.confirm?.message}>
              <Input
                id="confirm"
                type="password"
                autoComplete="new-password"
                {...form.register('confirm')}
              />
            </Field>

            <fieldset className="space-y-2">
              <legend className="text-sm font-medium">Accept streams over</legend>
              <p className="text-sm text-muted-foreground">
                All off until you switch them on, here or later. Every stream needs a credential
                either way.
              </p>
              {protocols.map((p) => (
                <Controller
                  key={p.name}
                  control={form.control}
                  name={p.name}
                  render={({ field }) => (
                    <div className="flex items-center gap-3">
                      <Checkbox
                        aria-label={p.label}
                        checked={field.value}
                        onCheckedChange={(checked) => {
                          field.onChange(checked)
                        }}
                      />
                      <span className="text-sm">
                        <span className="font-medium">{p.label}</span>{' '}
                        <span className="text-muted-foreground">({p.hint})</span>
                      </span>
                    </div>
                  )}
                />
              ))}
            </fieldset>

            {setup.isError && (
              <Alert variant="destructive" role="alert">
                <AlertDescription>{setup.error.message}</AlertDescription>
              </Alert>
            )}
            <Button type="submit" className="w-full" disabled={setup.isPending}>
              {setup.isPending ? 'Setting up…' : 'Create admin and finish setup'}
            </Button>
          </form>
        </CardContent>
      </Card>
    </div>
  )
}

function Field({
  id,
  label,
  error,
  children,
}: {
  id: string
  label: string
  error?: string | undefined
  children: React.ReactNode
}) {
  return (
    <div className="space-y-1.5">
      <Label htmlFor={id}>{label}</Label>
      {children}
      {error && (
        <p className="text-sm text-destructive" role="alert">
          {error}
        </p>
      )}
    </div>
  )
}
