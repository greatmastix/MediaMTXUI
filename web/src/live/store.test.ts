import { describe, expect, it } from 'vitest'

import { snapshot } from '@/test/fakeEventSource'

import {
  applyEvent,
  initialState,
  listItems,
  mergeSamples,
  type LiveState,
  type Sample,
} from './store'

const apply = (state: LiveState, type: string, data: unknown) =>
  applyEvent(state, type, JSON.stringify(data))

const sample = (t: number): Sample => ({
  t,
  inBps: 1,
  outBps: 2,
  paths: 1,
  online: 1,
  readers: 0,
  clients: 0,
})

describe('applyEvent', () => {
  it('loads a snapshot', () => {
    const s = apply(
      initialState,
      'snapshot',
      snapshot(
        {
          info: { value: { version: 'v1.21.1', started: '2026-09-29T09:00:00Z' } },
          paths: { items: [{ name: 'b' }, { name: 'a' }] },
          rtmpConns: { available: false },
          future: { items: [{ id: 'x' }] },
        },
        { reachable: true },
        { a: { inBps: 8, outBps: 16 } },
      ),
    )
    expect(s.status?.reachable).toBe(true)
    expect(s.info?.version).toBe('v1.21.1')
    expect(listItems(s.lists, 'paths')?.map((p) => p.name)).toEqual(['a', 'b'])
    expect(s.lists.rtmpConns).toEqual({ available: false, truncated: false, items: new Map() })
    expect(s.rates.get('a')).toEqual({ inBps: 8, outBps: 16 })
    expect(Object.keys(s.lists)).not.toContain('future')
  })

  it('applies item-level updates, resets and switch-offs', () => {
    let s = apply(
      initialState,
      'snapshot',
      snapshot({ paths: { items: [{ name: 'a' }, { name: 'b' }] } }),
    )
    const before = s.lists.paths
    s = apply(s, 'update', {
      kind: 'paths',
      available: true,
      upsert: [{ name: 'c', online: true }],
      remove: ['a'],
    })
    expect(listItems(s.lists, 'paths')?.map((p) => p.name)).toEqual(['b', 'c'])
    expect(before?.items.size).toBe(2) // the previous state is untouched

    s = apply(s, 'update', { kind: 'paths', available: true, reset: true, upsert: [{ name: 'z' }] })
    expect(listItems(s.lists, 'paths')?.map((p) => p.name)).toEqual(['z'])

    s = apply(s, 'update', { kind: 'rtspSessions', available: false, reset: true })
    expect(s.lists.rtspSessions?.available).toBe(false)

    s = apply(s, 'update', { kind: 'info', available: true, value: { version: 'v1.21.1' } })
    expect(s.info?.version).toBe('v1.21.1')
    expect(apply(s, 'update', { kind: 'unknown', available: true })).toBe(s)
  })

  it('tracks status, samples and rates, and ignores unknown events', () => {
    let s = apply(initialState, 'status', {
      reachable: false,
      since: '2026-09-29T10:00:00Z',
      error: 'down',
    })
    expect(s.status).toEqual({ reachable: false, since: '2026-09-29T10:00:00Z', error: 'down' })
    s = apply(s, 'sample', sample(2))
    s = apply(s, 'sample', sample(1))
    expect(s.samples.map((x) => x.t)).toEqual([1, 2])
    s = apply(s, 'rates', { t: 1, paths: { a: { inBps: 1, outBps: 2 } } })
    expect(s.rates.get('a')?.outBps).toBe(2)
    expect(apply(s, 'something-new', {})).toBe(s)
  })

  it("keeps the sidecar's extras from the snapshot and their updates", () => {
    let s = apply(initialState, 'snapshot', {
      status: { reachable: true, since: '2026-09-29T10:00:00Z' },
      lists: {},
      rates: null,
      extras: { exposure: { installed: false } },
    })
    expect(s.extras).toEqual({ exposure: { installed: false } })
    s = apply(s, 'extra', { kind: 'exposure', value: { installed: true } })
    expect(s.extras.exposure).toEqual({ installed: true })
    s = apply(s, 'snapshot', {
      status: { reachable: true, since: '2026-09-29T10:00:00Z' },
      lists: {},
      rates: null,
    })
    expect(s.extras).toEqual({})
  })

  it('rejects envelopes that do not match', () => {
    expect(() => apply(initialState, 'update', { kind: 'paths' })).toThrow()
    expect(() => applyEvent(initialState, 'status', 'not json')).toThrow()
  })
})

describe('mergeSamples', () => {
  it('orders, de-duplicates and keeps the last hour', () => {
    const many = Array.from({ length: 800 }, (_, i) => sample(i))
    const merged = mergeSamples(many.slice(0, 500), many.slice(400))
    expect(merged).toHaveLength(720)
    expect(merged[0]?.t).toBe(80)
    expect(merged.at(-1)?.t).toBe(799)
  })
})
