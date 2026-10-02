import { z } from 'zod'

import type { MediaMTX } from '@/api/mediamtx'

// The live view of MediaMTX, fed by the sidecar's event stream (GET /api/v1/events): a snapshot first, then item-level
// updates of each list, MediaMTX's reachability, and history samples and per-path bitrates every 5 s. The sidecar's
// envelopes are validated here; the items inside are MediaMTX's own shapes, typed from the generated OpenAPI types.

/** The lists the sidecar mirrors, with the MediaMTX schema of their items. */
export interface ListItems {
  paths: MediaMTX['Path']
  rtspConns: MediaMTX['RTSPConn']
  rtspSessions: MediaMTX['RTSPSession']
  rtspsConns: MediaMTX['RTSPConn']
  rtspsSessions: MediaMTX['RTSPSession']
  rtmpConns: MediaMTX['RTMPConn']
  rtmpsConns: MediaMTX['RTMPConn']
  srtConns: MediaMTX['SRTConn']
  webrtcSessions: MediaMTX['WebRTCSession']
  hlsMuxers: MediaMTX['HLSMuxer']
  hlsSessions: MediaMTX['HLSSession']
  moqSessions: MediaMTX['MoQSession']
}
export type ListKind = keyof ListItems

/** The field that identifies an item of each list. */
export const keyField: Record<ListKind, string> = {
  paths: 'name',
  rtspConns: 'id',
  rtspSessions: 'id',
  rtspsConns: 'id',
  rtspsSessions: 'id',
  rtmpConns: 'id',
  rtmpsConns: 'id',
  srtConns: 'id',
  webrtcSessions: 'id',
  hlsMuxers: 'path',
  hlsSessions: 'id',
  moqSessions: 'id',
}
const isListKind = (k: string): k is ListKind => Object.hasOwn(keyField, k)

export interface LiveList<T> {
  /** False while the protocol's server is switched off in MediaMTX. */
  available: boolean
  /** More items exist than the sidecar mirrors. */
  truncated: boolean
  items: ReadonlyMap<string, T>
}

export type Lists = { readonly [K in ListKind]?: LiveList<ListItems[K]> }

export interface PathRate {
  inBps: number
  outBps: number
}

const statusSchema = z.object({
  reachable: z.boolean(),
  since: z.string(),
  error: z.string().optional(),
  polledAt: z.string().optional(),
})
export type MediaMTXStatus = z.infer<typeof statusSchema>

export const sampleSchema = z.object({
  t: z.number(),
  inBps: z.number().nullable(),
  outBps: z.number().nullable(),
  paths: z.number(),
  online: z.number(),
  readers: z.number(),
  clients: z.number(),
})
export type Sample = z.infer<typeof sampleSchema>

const itemSchema = z.record(z.string(), z.unknown())
const ratesSchema = z.object({
  t: z.number(),
  paths: z.record(z.string(), z.object({ inBps: z.number(), outBps: z.number() })),
})
const snapshotSchema = z.object({
  status: statusSchema,
  lists: z.record(
    z.string(),
    z.object({
      available: z.boolean(),
      truncated: z.boolean().optional(),
      items: z.array(itemSchema).optional(),
      value: itemSchema.optional(),
    }),
  ),
  rates: ratesSchema.nullable(),
  extras: z.record(z.string(), z.unknown()).optional(),
})
const extraSchema = z.object({ kind: z.string(), value: z.unknown() })
const updateSchema = z.object({
  kind: z.string(),
  available: z.boolean(),
  truncated: z.boolean().optional(),
  reset: z.boolean().optional(),
  upsert: z.array(itemSchema).optional(),
  remove: z.array(z.string()).optional(),
  value: itemSchema.optional(),
})

export type Connection = 'connecting' | 'open' | 'reconnecting'

export interface LiveState {
  /** The event stream's state. */
  connection: Connection
  /** Whether MediaMTX answers the sidecar; null until the first snapshot. */
  status: MediaMTXStatus | null
  info: MediaMTX['Info'] | null
  lists: Lists
  rates: ReadonlyMap<string, PathRate>
  /** When the rates were measured (Unix ms), or 0 before the first. */
  ratesAt: number
  /** The last hour, oldest first. */
  samples: readonly Sample[]
  /** The sidecar's own streamed values by kind (exposure: the exposure control status), unvalidated. */
  extras: Readonly<Record<string, unknown>>
}

export const initialState: LiveState = {
  connection: 'connecting',
  status: null,
  info: null,
  lists: {},
  rates: new Map(),
  ratesAt: 0,
  samples: [],
  extras: {},
}

const maxSamples = 720

function keyOf(kind: ListKind, item: Record<string, unknown>): string | undefined {
  const k = item[keyField[kind]]
  return typeof k === 'string' ? k : undefined
}

function toMap(kind: ListKind, items: Record<string, unknown>[] = []) {
  const m = new Map<string, unknown>()
  for (const it of items) {
    const k = keyOf(kind, it)
    if (k !== undefined) m.set(k, it)
  }
  return m
}

type AnyList = LiveList<unknown>

function withList(lists: Lists, kind: ListKind, list: AnyList): Lists {
  return { ...lists, [kind]: list }
}

/** Appends samples in time order, dropping duplicates and anything older than the last hour's worth. */
export function mergeSamples(have: readonly Sample[], add: readonly Sample[]): Sample[] {
  const byT = new Map<number, Sample>()
  for (const s of have) byT.set(s.t, s)
  for (const s of add) byT.set(s.t, s)
  const out = [...byT.values()].sort((a, b) => a.t - b.t)
  return out.length > maxSamples ? out.slice(out.length - maxSamples) : out
}

/**
 * Applies one server-sent event. Unknown event types and kinds are ignored, so an older UI keeps working against
 * a newer sidecar; an envelope that does not match its schema throws.
 */
export function applyEvent(state: LiveState, type: string, data: string): LiveState {
  switch (type) {
    case 'snapshot': {
      const snap = snapshotSchema.parse(JSON.parse(data))
      let lists: Lists = {}
      let info: LiveState['info'] = null
      for (const [kind, l] of Object.entries(snap.lists)) {
        if (kind === 'info') info = l.available ? (l.value ?? null) : null
        else if (isListKind(kind)) {
          lists = withList(lists, kind, {
            available: l.available,
            truncated: l.truncated ?? false,
            items: toMap(kind, l.items),
          })
        }
      }
      const rates = new Map(Object.entries(snap.rates?.paths ?? {}))
      return {
        ...state,
        status: snap.status,
        info,
        lists,
        rates,
        ratesAt: snap.rates?.t ?? 0,
        extras: snap.extras ?? {},
      }
    }
    case 'update': {
      const u = updateSchema.parse(JSON.parse(data))
      if (u.kind === 'info') {
        return { ...state, info: u.available ? (u.value ?? null) : null }
      }
      if (!isListKind(u.kind)) return state
      const prev = state.lists[u.kind] as AnyList | undefined
      const items = u.reset || !prev ? new Map<string, unknown>() : new Map(prev.items)
      for (const k of u.remove ?? []) items.delete(k)
      for (const it of u.upsert ?? []) {
        const k = keyOf(u.kind, it)
        if (k !== undefined) items.set(k, it)
      }
      const list: AnyList = { available: u.available, truncated: u.truncated ?? false, items }
      return { ...state, lists: withList(state.lists, u.kind, list) }
    }
    case 'status':
      return { ...state, status: statusSchema.parse(JSON.parse(data)) }
    case 'sample':
      return {
        ...state,
        samples: mergeSamples(state.samples, [sampleSchema.parse(JSON.parse(data))]),
      }
    case 'extra': {
      const e = extraSchema.parse(JSON.parse(data))
      return { ...state, extras: { ...state.extras, [e.kind]: e.value } }
    }
    case 'rates': {
      const r = ratesSchema.parse(JSON.parse(data))
      return { ...state, rates: new Map(Object.entries(r.paths)), ratesAt: r.t }
    }
    default:
      return state
  }
}

/** The items of a list in key order, or undefined while unknown. */
export function listItems<K extends ListKind>(lists: Lists, kind: K): ListItems[K][] | undefined {
  const l = lists[kind]
  if (!l) return undefined
  return [...l.items.entries()].sort(([a], [b]) => a.localeCompare(b)).map(([, v]) => v)
}
