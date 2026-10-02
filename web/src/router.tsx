import type { QueryClient } from '@tanstack/react-query'
import {
  createRootRouteWithContext,
  createRoute,
  createRouter,
  lazyRouteComponent,
  Link,
  Outlet,
  redirect,
  type RouterHistory,
} from '@tanstack/react-router'
import { z } from 'zod'

import { sessionQuery, setupQuery } from '@/api/sidecar'
import { AppShell } from '@/components/AppShell'
import { modeFor } from '@/lib/mode'
import { protocolById, protocols } from '@/lib/protocols'
import { keyField, type ListKind } from '@/live/store'
import { ConfigLayout } from '@/pages/config/ConfigLayout'
import { HistoryPage } from '@/pages/config/HistoryPage'
import { PathEditorPage, PathsConfigPage } from '@/pages/config/PathsConfigPage'
import { QuickSetupPage } from '@/pages/config/QuickSetupPage'
import { GlobalSettingsPage, PathDefaultsPage } from '@/pages/config/SettingsPages'
import { YamlPage } from '@/pages/config/YamlPage'
import { ConnectionsPage } from '@/pages/ConnectionsPage'
import { DashboardPage } from '@/pages/DashboardPage'
import { LoginPage } from '@/pages/LoginPage'
import { PathPage } from '@/pages/PathPage'
import { PathsPage } from '@/pages/PathsPage'
import { WatchPage } from '@/pages/WatchPage'
import { SetupPage } from '@/pages/SetupPage'

interface Context {
  queryClient: QueryClient
}

function NotFound() {
  return (
    <div className="grid min-h-dvh place-items-center p-6 text-center">
      <div className="space-y-2">
        <h1 className="font-heading text-xl font-semibold">Page not found</h1>
        <Link to="/" className="text-sm underline underline-offset-4">
          Back to the start
        </Link>
      </div>
    </div>
  )
}

const rootRoute = createRootRouteWithContext<Context>()({
  component: () => (
    <div className="min-h-dvh bg-background text-foreground">
      <Outlet />
    </div>
  ),
  notFoundComponent: NotFound,
})

/** Only same-site paths are followed after sign-in, never another origin. */
export function safeRedirect(to: string | undefined): string {
  return to?.startsWith('/') && !to.startsWith('//') && !to.startsWith('/\\') ? to : '/'
}

// Guards: setup comes first, then sign-in. They read the query cache (fetching only what is not cached yet), which
// sign-in, setup and sign-out update directly.
const setupRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/setup',
  beforeLoad: async ({ context }) => {
    if (!(await context.queryClient.query({ ...setupQuery, staleTime: 'static' })).required)
      throw redirect({ to: '/' })
  },
  component: SetupPage,
})

const loginRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/login',
  validateSearch: z.object({ redirect: z.string().max(2048).optional() }),
  beforeLoad: async ({ context, search }) => {
    if ((await context.queryClient.query({ ...setupQuery, staleTime: 'static' })).required)
      throw redirect({ to: '/setup' })
    if (await context.queryClient.query({ ...sessionQuery, staleTime: 'static' }))
      throw redirect({ href: safeRedirect(search.redirect) })
  },
  component: function Login() {
    return <LoginPage redirectTo={safeRedirect(loginRoute.useSearch().redirect)} />
  },
})

// Where an invited person sets their password; signed-in users go home.
const joinRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/join',
  beforeLoad: async ({ context }) => {
    if ((await context.queryClient.query({ ...setupQuery, staleTime: 'static' })).required)
      throw redirect({ to: '/setup' })
    if (await context.queryClient.query({ ...sessionQuery, staleTime: 'static' }))
      throw redirect({ to: '/' })
  },
  component: lazyRouteComponent(() => import('@/pages/JoinPage'), 'JoinPage'),
})

// A public stream's watch link: no sign-in, no setup check (it only ever shows public streams).
const PublicWatchPage = lazyRouteComponent(
  () => import('@/pages/PublicWatchPage'),
  'PublicWatchPage',
)
const publicWatchRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/s/$',
  component: function PublicWatch() {
    const { _splat } = publicWatchRoute.useParams()
    return <PublicWatchPage name={_splat ?? ''} />
  },
})

// Everything signed in lives under this layout, which runs the live event stream.
const appRoute = createRoute({
  getParentRoute: () => rootRoute,
  id: 'app',
  beforeLoad: async ({ context, location }) => {
    if ((await context.queryClient.query({ ...setupQuery, staleTime: 'static' })).required)
      throw redirect({ to: '/setup' })
    if (!(await context.queryClient.query({ ...sessionQuery, staleTime: 'static' }))) {
      throw redirect({
        to: '/login',
        search: { redirect: location.href === '/' ? undefined : location.href },
      })
    }
  },
  component: AppShell,
})

// Streamers have no dashboard, and the simple mode starts on the streams too.
const dashboardRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/',
  beforeLoad: async ({ context }) => {
    const s = await context.queryClient.query({ ...sessionQuery, staleTime: 'static' })
    if (s && modeFor(s.user.role) === 'streaming') throw redirect({ to: '/streams' })
  },
  component: DashboardPage,
})

const streamsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/streams',
  component: lazyRouteComponent(() => import('@/pages/StreamsPage'), 'StreamsPage'),
})

// Pages few people open (admins' pages, stream pages) load on demand, to keep the first load within budget.
const StreamPage = lazyRouteComponent(() => import('@/pages/StreamPage'), 'StreamPage')

const streamRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/streams/$id',
  component: function Stream() {
    const id = Number(streamRoute.useParams().id)
    return <StreamPage key={id} id={Number.isInteger(id) && id > 0 ? id : 0} />
  },
})

const peopleRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/people',
  component: lazyRouteComponent(() => import('@/pages/PeoplePage'), 'PeoplePage'),
})

// A path name may contain slashes, so a path's page is a splat under /paths. The list is the index route: a bare
// splat route would also match /paths itself, with an empty name.
const pathsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/paths',
})

const pathsIndexRoute = createRoute({
  getParentRoute: () => pathsRoute,
  path: '/',
  component: PathsPage,
})

const pathRoute = createRoute({
  getParentRoute: () => pathsRoute,
  path: '$',
  component: function Path() {
    const { _splat } = pathRoute.useParams()
    return <PathPage key={_splat} name={_splat ?? ''} />
  },
})

// Recordings: the list, and a path's timeline as a splat (path names may contain slashes).
const recordingsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/recordings',
})

const recordingsIndexRoute = createRoute({
  getParentRoute: () => recordingsRoute,
  path: '/',
  component: lazyRouteComponent(() => import('@/pages/RecordingsPage'), 'RecordingsPage'),
})

const RecordingPage = lazyRouteComponent(() => import('@/pages/RecordingPage'), 'RecordingPage')
const recordingRoute = createRoute({
  getParentRoute: () => recordingsRoute,
  path: '$',
  component: function Recording() {
    const { _splat } = recordingRoute.useParams()
    return <RecordingPage key={_splat} name={_splat ?? ''} />
  },
})

const accountRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/account',
  component: lazyRouteComponent(() => import('@/pages/AccountPage'), 'AccountPage'),
})

const logsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/logs',
  component: lazyRouteComponent(() => import('@/pages/LogsPage'), 'LogsPage'),
})

const backupsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/backups',
  component: lazyRouteComponent(() => import('@/pages/BackupsPage'), 'BackupsPage'),
})

const auditRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/audit',
  component: lazyRouteComponent(() => import('@/pages/AuditPage'), 'AuditPage'),
})

const connectionsIndexRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/connections',
  beforeLoad: () => {
    throw redirect({
      to: '/connections/$protocol',
      params: { protocol: protocols[0]?.id ?? 'rtsp' },
    })
  },
})

const listKinds = Object.keys(keyField) as [ListKind, ...ListKind[]]

const connectionsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/connections/$protocol',
  validateSearch: z.object({
    kind: z.enum(listKinds).optional().catch(undefined),
    id: z.string().max(256).optional().catch(undefined),
  }),
  component: function Connections() {
    const { protocol } = connectionsRoute.useParams()
    const selection = connectionsRoute.useSearch()
    const p = protocolById(protocol)
    return p ? <ConnectionsPage protocol={p} selection={selection} /> : <NotFound />
  },
})

const watchRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/watch',
  // path: open with this stream; ice=relay forces WebRTC to fail (testing the HLS fallback).
  validateSearch: z.object({
    path: z.string().max(256).optional().catch(undefined),
    ice: z.enum(['relay']).optional().catch(undefined),
  }),
  component: function Watch() {
    const { path, ice } = watchRoute.useSearch()
    return <WatchPage initialPath={path} policy={ice} />
  },
})

const credentialsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/credentials',
  component: lazyRouteComponent(() => import('@/pages/CredentialsPage'), 'CredentialsPage'),
})

const exposureRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/exposure',
  component: lazyRouteComponent(() => import('@/pages/ExposurePage'), 'ExposurePage'),
})

// Configuration (admins; the layout says so to others). The config pages load the settings catalog on demand.
const configRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/config',
  component: ConfigLayout,
})

const configIndexRoute = createRoute({
  getParentRoute: () => configRoute,
  path: '/',
  beforeLoad: () => {
    throw redirect({ to: '/config/quick' })
  },
})

const configQuickRoute = createRoute({
  getParentRoute: () => configRoute,
  path: '/quick',
  component: QuickSetupPage,
})

const configPathsRoute = createRoute({
  getParentRoute: () => configRoute,
  path: '/paths',
})

const configPathsIndexRoute = createRoute({
  getParentRoute: () => configPathsRoute,
  path: '/',
  component: PathsConfigPage,
})

const configPathRoute = createRoute({
  getParentRoute: () => configPathsRoute,
  path: '$',
  component: function ConfigPath() {
    const { _splat } = configPathRoute.useParams()
    return <PathEditorPage name={_splat ?? ''} />
  },
})

const configNewPathRoute = createRoute({
  getParentRoute: () => configRoute,
  path: '/new-path',
  component: function NewPath() {
    return <PathEditorPage />
  },
})

const configGlobalRoute = createRoute({
  getParentRoute: () => configRoute,
  path: '/global',
  component: GlobalSettingsPage,
})

const configDefaultsRoute = createRoute({
  getParentRoute: () => configRoute,
  path: '/path-defaults',
  component: PathDefaultsPage,
})

const configYamlRoute = createRoute({
  getParentRoute: () => configRoute,
  path: '/yaml',
  component: YamlPage,
})

const configHistoryRoute = createRoute({
  getParentRoute: () => configRoute,
  path: '/history',
  validateSearch: z.object({ id: z.number().int().positive().optional().catch(undefined) }),
  component: function History() {
    return <HistoryPage selected={configHistoryRoute.useSearch().id} />
  },
})

const routeTree = rootRoute.addChildren([
  setupRoute,
  loginRoute,
  joinRoute,
  publicWatchRoute,
  appRoute.addChildren([
    dashboardRoute,
    streamsRoute,
    streamRoute,
    peopleRoute,
    pathsRoute.addChildren([pathsIndexRoute, pathRoute]),
    recordingsRoute.addChildren([recordingsIndexRoute, recordingRoute]),
    accountRoute,
    auditRoute,
    logsRoute,
    backupsRoute,
    connectionsIndexRoute,
    connectionsRoute,
    credentialsRoute,
    exposureRoute,
    watchRoute,
    configRoute.addChildren([
      configIndexRoute,
      configQuickRoute,
      configPathsRoute.addChildren([configPathsIndexRoute, configPathRoute]),
      configNewPathRoute,
      configGlobalRoute,
      configDefaultsRoute,
      configYamlRoute,
      configHistoryRoute,
    ]),
  ]),
])

export function createAppRouter(queryClient: QueryClient, history?: RouterHistory) {
  return createRouter({ routeTree, context: { queryClient }, history })
}

declare module '@tanstack/react-router' {
  interface Register {
    router: ReturnType<typeof createAppRouter>
  }
}
