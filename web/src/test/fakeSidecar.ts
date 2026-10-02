import { vi } from 'vitest'

import type { Session, Status } from '@/api/sidecar'

// A fake sidecar for component tests: fetch calls are routed by method and path over shared state, and recorded.

export interface FakeSnapshot {
  id: number
  at: string
  sha256: string
  author: string
  reason: string
  parentId: number | null
  content: string
}

export interface FakeState {
  setupRequired: boolean
  session: Session | null
  password: string
  status: Status
  /** mediamtx.yml as data; the fake renders it as JSON text for the raw editor. */
  settings: Record<string, unknown>
  snapshots: FakeSnapshot[]
  credentials: Record<string, unknown>[]
}

const render = (settings: Record<string, unknown>) => JSON.stringify(settings, null, 2) + '\n'

export const initialSettings = (): Record<string, unknown> => ({
  authMethod: 'http',
  api: true,
  logLevel: 'info',
  pathDefaults: {},
  paths: { cam1: { source: 'rtsp://user:secret@10.0.0.5/live' }, all_others: null },
})

export interface Call {
  method: string
  path: string
  headers: Headers
  body: unknown
}

export const adminSession: Session = {
  user: { id: 1, username: 'admin', role: 'admin' },
  csrfToken: 'csrf-token-1',
  authMethod: 'password',
  expiresAt: '2026-10-06T00:00:00Z',
}

export const healthyStatus: Status = {
  checkedAt: '2026-09-29T10:00:00Z',
  reachable: true,
  version: '1.21.1',
  expectedVersion: '1.21.1',
  apiProtected: true,
  warnings: [],
}

const json = (status: number, body: unknown) => Response.json(body, { status })
const error = (status: number, kind: string, message: string) =>
  json(status, { error: kind, message })

export function fakeSidecar(initial: Partial<FakeState> = {}) {
  const state: FakeState = {
    setupRequired: false,
    session: null,
    password: 'correct horse battery',
    status: healthyStatus,
    settings: initialSettings(),
    snapshots: [],
    credentials: [
      {
        name: 'devpub',
        kind: 'password',
        actions: ['publish'],
        paths: ['test'],
        sources: [],
        expiresAt: null,
        revokedAt: null,
        lastUsedAt: '2026-09-29T10:00:00Z',
        createdAt: '2026-09-29T09:00:00Z',
        createdBy: 'cli',
        state: 'active',
      },
    ],
    ...initial,
  }
  const calls: Call[] = []
  const snapshot = (reason: string, author = 'admin') => {
    const prev = state.snapshots[0]
    const id = (prev?.id ?? 0) + 1
    const snap = {
      id,
      at: new Date(Date.UTC(2026, 8, 29, 10, id)).toISOString(),
      sha256: `sha-${String(id)}`,
      author,
      reason,
      parentId: prev?.id ?? null,
      content: render(state.settings),
    }
    state.snapshots.unshift(snap)
    return snap
  }
  if (state.snapshots.length === 0) snapshot('initial config', 'system')
  const meta = ({ id, at, sha256, author, reason, parentId }: FakeSnapshot) => ({
    id,
    at,
    sha256,
    author,
    reason,
    parentId,
  })
  const written = (reason: string) => {
    const snap = snapshot(reason)
    return json(200, { changed: true, sha256: snap.sha256, snapshot: meta(snap) })
  }
  const section = (key?: string): Record<string, unknown> => {
    if (!key) return state.settings
    const cur = state.settings[key]
    if (cur && typeof cur === 'object') return cur as Record<string, unknown>
    const fresh: Record<string, unknown> = {}
    state.settings[key] = fresh
    return fresh
  }
  const patch = (key: string | undefined, body: Record<string, unknown>) => {
    const set = (body.set ?? {}) as Record<string, unknown>
    if (set.readTimeout === 'bad') {
      return error(422, 'invalid_config', 'MediaMTX rejects the config: invalid duration: bad')
    }
    const target = section(key)
    Object.assign(target, set)
    for (const k of (body.remove ?? []) as string[]) Reflect.deleteProperty(target, k)
    return written(`${key ?? 'global settings'} changed`)
  }
  const routes: Record<
    string,
    (body: Record<string, unknown>, headers: Headers, path: string) => Response
  > = {
    'GET /api/v1/health': () =>
      json(200, { status: 'ok', version: '0.1.0', mediamtxVersion: '1.21.1' }),
    'GET /api/v1/setup': () => json(200, { required: state.setupRequired }),
    'POST /api/v1/setup': (body) => {
      if (!state.setupRequired) return error(404, 'not_found', 'Setup has already been completed.')
      if (body.token !== 'TOKEN') return error(401, 'invalid_token', 'The setup token is wrong.')
      state.setupRequired = false
      state.session = {
        ...adminSession,
        user: { ...adminSession.user, username: String(body.username) },
      }
      return json(201, state.session)
    },
    'POST /api/v1/auth/login': (body) => {
      if (body.password !== state.password)
        return error(401, 'invalid_credentials', 'Wrong username or password.')
      state.session = adminSession
      return json(200, state.session)
    },
    'GET /api/v1/session': () =>
      state.session ? json(200, state.session) : error(401, 'unauthorized', 'Sign in first.'),
    'GET /api/v1/status': () => json(200, state.status),
    'GET /api/v1/metrics/history': () =>
      state.session
        ? json(200, {
            intervalSeconds: 5,
            samples: [
              {
                t: Date.now() - 10_000,
                inBps: 1000,
                outBps: 3000,
                paths: 1,
                online: 1,
                readers: 2,
                clients: 3,
              },
              {
                t: Date.now() - 5000,
                inBps: 2000,
                outBps: 6000,
                paths: 1,
                online: 1,
                readers: 2,
                clients: 3,
              },
            ],
          })
        : error(401, 'unauthorized', 'Sign in first.'),
    'GET /api/v1/config': () => {
      const snap = state.snapshots[0]
      return json(200, {
        content: render(state.settings),
        sha256: snap?.sha256 ?? 'sha-0',
        snapshot: snap ? meta(snap) : null,
        settings: state.settings,
        locked: { authMethod: 'MediaMTX asks the sidecar about every authentication.' },
        publicHost: 'mtx.example.com',
      })
    },
    'PATCH /api/v1/config/global': (body) => patch(undefined, body),
    'PATCH /api/v1/config/path-defaults': (body) => patch('pathDefaults', body),
    'POST /api/v1/config/validate': (body) =>
      String(body.content).includes('bogus')
        ? error(422, 'invalid_config', 'MediaMTX rejects the config: bogus')
        : json(200, { valid: true }),
    'PUT /api/v1/config': (body) => {
      if (body.sha256 !== state.snapshots[0]?.sha256) {
        return error(409, 'conflict', 'mediamtx.yml has changed since you loaded it.')
      }
      state.settings = JSON.parse(String(body.content)) as Record<string, unknown>
      return written(
        typeof body.reason === 'string' && body.reason ? body.reason : 'edited as YAML',
      )
    },
    'GET /api/v1/config/snapshots': () => json(200, state.snapshots.map(meta)),
    'PUT /api/v1/config/paths/*': (body, _, path) => {
      const name = decodeURIComponent(path.replace('/api/v1/config/paths/', ''))
      section('paths')[name] = body.config
      return written(`path ${name} saved`)
    },
    'DELETE /api/v1/config/paths/*': (_, __, path) => {
      const name = decodeURIComponent(path.replace('/api/v1/config/paths/', ''))
      const paths = section('paths')
      if (!(name in paths))
        return error(404, 'not_found', 'There is no such entry in mediamtx.yml.')
      Reflect.deleteProperty(paths, name)
      return written(`path ${name} deleted`)
    },
    'GET /api/v1/config/snapshots/*': (_, __, path) => {
      const snap = state.snapshots.find((x) => path.endsWith(`/${String(x.id)}`))
      return snap ? json(200, snap) : error(404, 'not_found', 'No such snapshot.')
    },
    'POST /api/v1/config/snapshots/*': (_, __, path) => {
      const snap = state.snapshots.find((x) => path.endsWith(`/${String(x.id)}/restore`))
      if (!snap) return error(404, 'not_found', 'No such snapshot.')
      state.settings = JSON.parse(snap.content) as Record<string, unknown>
      return written(`restored snapshot ${String(snap.id)}`)
    },
    'GET /api/v1/credentials': () => json(200, state.credentials),
    'POST /api/v1/credentials': (body) => {
      if (state.credentials.some((c) => c.name === body.name)) {
        return error(409, 'exists', 'A credential with this name already exists.')
      }
      const c = {
        name: body.name,
        kind: body.kind,
        actions: body.actions,
        paths: body.paths,
        sources: body.sources,
        expiresAt: body.expiresInHours ? '2026-10-01T10:00:00Z' : null,
        revokedAt: null,
        lastUsedAt: null,
        createdAt: '2026-09-29T10:00:00Z',
        createdBy: 'admin',
        state: 'active',
      }
      state.credentials.push(c)
      return json(201, { ...c, secret: 'SECRETSECRETSECRET2345' })
    },
    'POST /api/v1/credentials/*': (_, __, path) => {
      const name = decodeURIComponent(path.split('/')[4] ?? '')
      const c = state.credentials.find((x) => x.name === name)
      if (!c) return error(404, 'not_found', 'No such credential.')
      c.state = 'revoked'
      c.revokedAt = '2026-09-29T11:00:00Z'
      return json(200, { kicked: 2 })
    },
    'POST /api/v1/config/drift/dismiss': () => {
      state.status = {
        ...state.status,
        warnings: state.status.warnings.filter((w) => w.code !== 'config_drift'),
      }
      return new Response(null, { status: 204 })
    },
    'POST /api/v1/auth/logout': (_, headers) => {
      if (headers.get('X-CSRF-Token') !== state.session?.csrfToken) {
        return error(403, 'csrf', 'Missing or wrong CSRF token.')
      }
      state.session = null
      return new Response(null, { status: 204 })
    },
  }
  const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(input instanceof Request ? input.url : input.toString(), 'http://ui.test')
    const method = init?.method ?? 'GET'
    const headers = new Headers(init?.headers)
    const body: unknown = typeof init?.body === 'string' ? JSON.parse(init.body) : undefined
    calls.push({ method, path: url.pathname, headers, body })
    const key = `${method} ${url.pathname}`
    const route =
      routes[key] ??
      Object.entries(routes).find(([k]) => k.endsWith('/*') && key.startsWith(k.slice(0, -1)))?.[1]
    return Promise.resolve(
      route
        ? route((body ?? {}) as Record<string, unknown>, headers, url.pathname)
        : error(404, 'not_found', 'not found'),
    )
  })
  vi.stubGlobal('fetch', fetchMock)
  return { state, calls }
}
