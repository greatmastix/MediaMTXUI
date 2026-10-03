import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link, Outlet, useNavigate, useRouterState } from '@tanstack/react-router'
import {
  Cable,
  Clapperboard,
  Film,
  Globe,
  KeyRound,
  LayoutDashboard,
  LogOut,
  type LucideIcon,
  Menu,
  Monitor,
  MonitorPlay,
  Moon,
  Radio,
  Archive,
  FileText,
  ScrollText,
  SlidersHorizontal,
  Sun,
  UserRound,
  Users,
  Waypoints,
} from 'lucide-react'
import { useCallback, useEffect, useState } from 'react'

import { ApiError } from '@/api/client'
import { dismissDrift } from '@/api/config'
import { changeSession, logout, sessionQuery, statusQuery, type Session } from '@/api/sidecar'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button, buttonVariants } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuLabel,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { Sheet, SheetContent, SheetTitle, SheetTrigger } from '@/components/ui/sheet'
import { useNow } from '@/hooks/useNow'
import { formatDuration } from '@/lib/format'
import { setMode, useMode, type Mode } from '@/lib/mode'
import { atLeast, type Role } from '@/lib/roles'
import { setTheme, storedTheme, type Theme } from '@/lib/theme'
import { cn } from '@/lib/utils'
import { StepUpGate } from '@/components/StepUp'
import { LiveProvider } from '@/live/context'
import { useLive } from '@/live/useLive'

interface NavItem {
  to:
    | '/'
    | '/streams'
    | '/watch'
    | '/paths'
    | '/recordings'
    | '/connections'
    | '/credentials'
    | '/people'
    | '/exposure'
    | '/config'
    | '/account'
    | '/audit'
    | '/logs'
    | '/backups'
  label: string
  icon: LucideIcon
  minRole: Role
  /** Also in the simple ("streaming") mode. */
  simple?: boolean
  /** Only while exposure control is on. */
  exposure?: boolean
}

const nav: NavItem[] = [
  { to: '/', label: 'Dashboard', icon: LayoutDashboard, minRole: 'viewer' },
  { to: '/streams', label: 'Streams', icon: Clapperboard, minRole: 'streamer', simple: true },
  { to: '/watch', label: 'Watch', icon: MonitorPlay, minRole: 'streamer', simple: true },
  { to: '/paths', label: 'Paths', icon: Waypoints, minRole: 'viewer' },
  { to: '/recordings', label: 'Recordings', icon: Film, minRole: 'viewer' },
  { to: '/connections', label: 'Connections', icon: Cable, minRole: 'operator' },
  { to: '/credentials', label: 'Credentials', icon: KeyRound, minRole: 'admin' },
  { to: '/people', label: 'People', icon: Users, minRole: 'admin' },
  { to: '/exposure', label: 'Exposure', icon: Globe, minRole: 'admin', exposure: true },
  { to: '/config', label: 'Configuration', icon: SlidersHorizontal, minRole: 'admin' },
  { to: '/audit', label: 'Audit log', icon: ScrollText, minRole: 'admin' },
  { to: '/logs', label: 'Logs', icon: FileText, minRole: 'admin' },
  { to: '/backups', label: 'Backups', icon: Archive, minRole: 'admin' },
  { to: '/account', label: 'Account', icon: UserRound, minRole: 'streamer', simple: true },
]

/** The signed-in layout: navigation, header and the live event stream for everything below it. */
export function AppShell() {
  const queryClient = useQueryClient()
  const navigate = useNavigate()
  const href = useRouterState({ select: (s) => s.location.href })

  // However the session ends (signed out elsewhere, expired, a 401 from any request), go to sign-in and come back
  // here afterwards. What was loaded for this session goes once nothing shows it any more.
  const endSession = useCallback(() => {
    queryClient.setQueryData(sessionQuery.queryKey, null)
    void navigate({ to: '/login', search: { redirect: href } }).then(() => {
      changeSession(queryClient, null)
    })
  }, [queryClient, navigate, href])
  const checkSession = useCallback(() => {
    void queryClient.query({ ...sessionQuery, staleTime: 0 }).then(
      (s) => {
        if (!s) endSession()
      },
      () => undefined,
    )
  }, [queryClient, endSession])
  useEffect(
    () =>
      queryClient.getQueryCache().subscribe((e) => {
        if (
          e.type === 'updated' &&
          e.action.type === 'error' &&
          e.action.error instanceof ApiError &&
          e.action.error.status === 401
        ) {
          endSession()
        }
      }),
    [queryClient, endSession],
  )

  return (
    <LiveProvider onSessionEnded={endSession} onRefused={checkSession}>
      <StepUpGate />
      <a
        href="#main"
        className="sr-only z-50 rounded-md bg-background px-3 py-2 focus:not-sr-only focus:fixed focus:top-2 focus:left-2"
      >
        Skip to content
      </a>
      <div className="min-h-dvh bg-background text-foreground md:grid md:grid-cols-[15rem_minmax(0,1fr)]">
        <aside className="sticky top-0 hidden h-dvh flex-col gap-2 border-r bg-sidebar p-3 text-sidebar-foreground md:flex">
          <Brand />
          <Nav />
        </aside>
        <div className="flex min-w-0 flex-col">
          <Header />
          <main id="main" tabIndex={-1} className="flex-1 space-y-6 p-4 outline-none md:p-6">
            <Banners />
            <Outlet />
          </main>
        </div>
      </div>
    </LiveProvider>
  )
}

function Brand() {
  return (
    <div className="flex items-center gap-3 px-2 py-1.5 font-heading text-[15px] font-semibold tracking-tight">
      <span className="grid size-8 shrink-0 place-items-center rounded-[9px] bg-signal/15">
        <Radio className="size-[18px] text-signal" aria-hidden />
      </span>
      MediaMTX UI
    </div>
  )
}

function Nav({ onNavigate }: { onNavigate?: () => void }) {
  const { data: session } = useQuery(sessionQuery)
  const role = session?.user.role ?? 'streamer'
  const mode = useMode(role)
  const { data: status } = useQuery({ ...statusQuery, enabled: role === 'admin' })
  return (
    <nav aria-label="Main" className="flex flex-col gap-1">
      {nav
        .filter(
          (item) =>
            atLeast(role, item.minRole) &&
            (mode === 'server' || item.simple) &&
            (!item.exposure || status?.exposureControl === true),
        )
        .map((item) => (
          <Link
            key={item.to}
            to={item.to}
            onClick={onNavigate}
            activeOptions={{ exact: item.to === '/' }}
            className="flex items-center gap-2 rounded-md px-2.5 py-1.5 text-sm text-muted-foreground hover:bg-sidebar-accent hover:text-sidebar-accent-foreground aria-[current=page]:bg-sidebar-accent aria-[current=page]:font-medium aria-[current=page]:text-sidebar-accent-foreground aria-[current=page]:shadow-[inset_2px_0_0_var(--signal)] [&[aria-current=page]>svg]:text-signal"
          >
            <item.icon className="size-4" aria-hidden />
            {item.label}
          </Link>
        ))}
    </nav>
  )
}

function Header() {
  const [open, setOpen] = useState(false)
  return (
    <header className="sticky top-0 z-20 flex h-14 items-center gap-2 border-b bg-background/95 px-3 backdrop-blur md:px-6">
      <Sheet open={open} onOpenChange={setOpen}>
        <SheetTrigger
          render={
            <Button
              variant="ghost"
              size="icon"
              className="md:hidden"
              aria-label="Open navigation"
            />
          }
        >
          <Menu />
        </SheetTrigger>
        <SheetContent side="left" className="w-64 gap-2 p-3">
          <SheetTitle render={<div />}>
            <Brand />
          </SheetTitle>
          <Nav
            onNavigate={() => {
              setOpen(false)
            }}
          />
        </SheetContent>
      </Sheet>
      <div className="flex-1" />
      <ModeSwitch />
      <LiveIndicator />
      <ThemeMenu />
      <Account />
    </header>
  )
}

/** Streaming (simple: streams and watching) or Server (everything); not offered to streamers, who only have the first. */
function ModeSwitch() {
  const navigate = useNavigate()
  const { data: session } = useQuery(sessionQuery)
  const role = session?.user.role ?? 'streamer'
  const mode = useMode(role)
  if (role === 'streamer') return null
  const pick = (m: Mode) => {
    if (m === mode) return
    setMode(m)
    void navigate({ to: m === 'streaming' ? '/streams' : '/' })
  }
  return (
    <div role="group" aria-label="Mode" className="flex rounded-md border p-0.5 text-[12.5px]">
      {(
        [
          ['streaming', 'Streaming'],
          ['server', 'Server'],
        ] as const
      ).map(([m, label]) => (
        <button
          key={m}
          type="button"
          aria-pressed={mode === m}
          className="rounded px-2 py-0.5 text-muted-foreground aria-pressed:bg-accent aria-pressed:font-medium aria-pressed:text-foreground"
          onClick={() => {
            pick(m)
          }}
        >
          {label}
        </button>
      ))}
    </div>
  )
}

type IndicatorState = 'live' | 'connecting' | 'reconnecting' | 'degraded'

// As on the server dashboard: a pulsing red dot while live, grey while (re)connecting.
const indicator: Record<IndicatorState, { label: string; dot: string }> = {
  live: { label: 'Live', dot: 'bg-signal live-pulse' },
  connecting: { label: 'Connecting…', dot: 'bg-muted-foreground animate-pulse' },
  reconnecting: { label: 'Reconnecting…', dot: 'bg-muted-foreground animate-pulse' },
  degraded: { label: 'MediaMTX down', dot: 'bg-warning' },
}

/** The event stream's state: live, (re)connecting, or connected but MediaMTX itself not answering. */
export function LiveIndicator() {
  const { connection, status } = useLive()
  let state: IndicatorState = connection === 'open' ? 'live' : connection
  if (connection === 'open' && status && !status.reachable) state = 'degraded'
  const { label, dot } = indicator[state]
  return (
    <div
      role="status"
      data-testid="live-indicator"
      data-state={state}
      className="flex items-center gap-2 px-1.5 text-[12.5px] text-muted-foreground"
    >
      <span className={cn('size-[7px] rounded-full', dot)} aria-hidden />
      {label}
    </div>
  )
}

function ThemeMenu() {
  const [theme, setThemeState] = useState<Theme>(storedTheme)
  const Icon = theme === 'light' ? Sun : theme === 'dark' ? Moon : Monitor
  return (
    <DropdownMenu>
      <DropdownMenuTrigger render={<Button variant="ghost" size="icon" aria-label="Theme" />}>
        <Icon />
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end">
        <DropdownMenuGroup>
          <DropdownMenuLabel>Theme</DropdownMenuLabel>
          <DropdownMenuRadioGroup
            value={theme}
            onValueChange={(v: Theme) => {
              setTheme(v)
              setThemeState(v)
            }}
          >
            <DropdownMenuRadioItem value="dark">Dark</DropdownMenuRadioItem>
            <DropdownMenuRadioItem value="light">Light</DropdownMenuRadioItem>
            <DropdownMenuRadioItem value="system">System</DropdownMenuRadioItem>
          </DropdownMenuRadioGroup>
        </DropdownMenuGroup>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

function Account() {
  const queryClient = useQueryClient()
  const navigate = useNavigate()
  const { data: session } = useQuery(sessionQuery)
  const signOut = useMutation({
    mutationFn: logout,
    onSuccess: async () => {
      queryClient.setQueryData<Session | null>(sessionQuery.queryKey, null)
      queryClient.removeQueries({ queryKey: statusQuery.queryKey })
      await navigate({ to: '/login' })
      changeSession(queryClient, null)
    },
  })
  const user = session?.user
  return (
    <div className="flex items-center gap-2 text-sm">
      {user && (
        <span data-testid="signed-in-as" className="hidden text-muted-foreground lg:inline">
          Signed in as <span className="font-medium text-foreground">{user.username}</span> (
          {user.role})
        </span>
      )}
      <Button
        variant="outline"
        size="sm"
        onClick={() => {
          signOut.mutate()
        }}
        disabled={signOut.isPending}
      >
        <LogOut aria-hidden />
        Sign out
      </Button>
    </div>
  )
}

/** For an outside edit of mediamtx.yml: look at it in the history, or acknowledge it. */
function DriftActions() {
  const queryClient = useQueryClient()
  const dismiss = useMutation({
    mutationFn: dismissDrift,
    onSuccess: () => queryClient.invalidateQueries({ queryKey: statusQuery.queryKey }),
  })
  return (
    <span className="mt-2 flex gap-2">
      <Link to="/config/history" className={buttonVariants({ size: 'sm', variant: 'outline' })}>
        Open the history
      </Link>
      <Button
        size="sm"
        variant="outline"
        disabled={dismiss.isPending}
        onClick={() => {
          dismiss.mutate()
        }}
      >
        Dismiss
      </Button>
    </span>
  )
}

/** Problems that concern every page: the sidecar's own warnings, and MediaMTX not answering. */
function Banners() {
  const { data: session } = useQuery(sessionQuery)
  // A streamer's status comes from its scoped event stream only; the sidecar's status is for viewers and up.
  const { data: st } = useQuery({
    ...statusQuery,
    enabled: atLeast(session?.user.role ?? 'streamer', 'viewer'),
  })
  const admin = session?.user.role === 'admin'
  const { status } = useLive()
  const now = useNow()
  return (
    <>
      {st?.unsafe && (
        <Alert variant="destructive" role="alert">
          <AlertTitle>The sidecar refuses to serve</AlertTitle>
          <AlertDescription>{st.unsafe}</AlertDescription>
        </Alert>
      )}
      {status && !status.reachable && (
        <Alert variant="destructive" role="alert" data-testid="mediamtx-unreachable">
          <AlertTitle>MediaMTX is not answering</AlertTitle>
          <AlertDescription>
            {status.error ? `${status.error}. ` : ''}
            For {formatDuration((now - Date.parse(status.since)) / 1000)} now. The pages show the
            last known state and catch up by themselves once MediaMTX is back.
          </AlertDescription>
        </Alert>
      )}
      {st?.warnings.map((w) => (
        <Alert key={w.code} role="status" data-testid={`warning-${w.code}`}>
          <AlertDescription>
            {w.message}
            {w.code === 'config_drift' && admin && <DriftActions />}
          </AlertDescription>
        </Alert>
      ))}
    </>
  )
}
