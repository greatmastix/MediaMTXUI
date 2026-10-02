import { vi } from 'vitest'

// A controllable EventSource (jsdom has none): tests open streams, deliver events and fail them.

type Listener = (e: MessageEvent<string>) => void

export class FakeEventSource {
  static readonly CONNECTING = 0
  static readonly OPEN = 1
  static readonly CLOSED = 2
  static instances: FakeEventSource[] = []

  readonly url: string
  readyState = FakeEventSource.CONNECTING
  onopen: (() => void) | null = null
  onerror: (() => void) | null = null
  private readonly listeners = new Map<string, Listener[]>()
  private seq = 0

  constructor(url: string) {
    this.url = url
    FakeEventSource.instances.push(this)
  }

  static get latest(): FakeEventSource {
    const es = FakeEventSource.instances.at(-1)
    if (!es) throw new Error('no EventSource was opened')
    return es
  }

  addEventListener(type: string, l: Listener) {
    this.listeners.set(type, [...(this.listeners.get(type) ?? []), l])
  }

  close() {
    this.readyState = FakeEventSource.CLOSED
  }

  open() {
    this.readyState = FakeEventSource.OPEN
    this.onopen?.()
  }

  emit(type: string, data: unknown) {
    const e = new MessageEvent<string>(type, {
      data: typeof data === 'string' ? data : JSON.stringify(data),
      lastEventId: `e-${++this.seq}`,
    })
    for (const l of this.listeners.get(type) ?? []) l(e)
  }

  /** A dropped stream the browser retries (closed = false) or a refused one it gives up on (closed = true). */
  fail(closed: boolean) {
    this.readyState = closed ? FakeEventSource.CLOSED : FakeEventSource.CONNECTING
    this.onerror?.()
  }
}

export function installFakeEventSource() {
  FakeEventSource.instances = []
  vi.stubGlobal('EventSource', FakeEventSource)
}

/** A snapshot as the sidecar sends it. */
export function snapshot(
  lists: Record<string, { available?: boolean; items?: unknown[]; value?: unknown }> = {},
  status: { reachable: boolean; since?: string; error?: string } = { reachable: true },
  rates: Record<string, { inBps: number; outBps: number }> | null = null,
) {
  const full: Record<string, unknown> = {}
  for (const [k, v] of Object.entries(lists)) full[k] = { available: v.available ?? true, ...v }
  return {
    status: { since: '2026-09-29T10:00:00Z', ...status },
    lists: full,
    rates: rates && { t: Date.now(), paths: rates },
  }
}
