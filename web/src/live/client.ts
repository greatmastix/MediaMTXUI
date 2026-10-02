import { z } from 'zod'

import { request } from '@/api/client'

import { applyEvent, initialState, mergeSamples, sampleSchema, type LiveState } from './store'

const historySchema = z.object({ intervalSeconds: z.number(), samples: z.array(sampleSchema) })

const eventTypes = ['snapshot', 'update', 'status', 'sample', 'rates', 'extra'] as const

export interface LiveHandlers {
  /** The stream said the session ended (signed out elsewhere, expired, role changed). */
  onSessionEnded?: () => void
  /** The stream was refused outright (401, 429, 503): the session may be gone. */
  onRefused?: () => void
}

/**
 * Owns the EventSource and the live state. The browser reconnects a dropped stream by itself, sending Last-Event-ID
 * so the sidecar replays what was missed; a stream refused outright is retried here with backoff, starting afresh
 * from a snapshot.
 */
export class LiveClient {
  private state: LiveState = initialState
  private readonly listeners = new Set<() => void>()
  private es: EventSource | null = null
  private timer: ReturnType<typeof setTimeout> | undefined
  private failures = 0
  private running = false
  private handlers: LiveHandlers = {}
  private readonly url: string
  private readonly historyUrl: string

  constructor(url = '/api/v1/events', historyUrl = '/api/v1/metrics/history') {
    this.url = url
    this.historyUrl = historyUrl
  }

  setHandlers(h: LiveHandlers) {
    this.handlers = h
  }

  start() {
    if (this.running) return
    this.running = true
    this.connect()
  }

  stop() {
    this.running = false
    clearTimeout(this.timer)
    this.es?.close()
    this.es = null
  }

  subscribe = (listener: () => void) => {
    this.listeners.add(listener)
    return () => {
      this.listeners.delete(listener)
    }
  }

  getState = () => this.state

  private set(next: LiveState) {
    if (next === this.state) return
    this.state = next
    for (const l of this.listeners) l()
  }

  private connect() {
    const es = new EventSource(this.url)
    this.es = es
    es.onopen = () => {
      this.failures = 0
      this.set({ ...this.state, connection: 'open' })
    }
    for (const type of eventTypes) {
      es.addEventListener(type, (e: MessageEvent<string>) => {
        this.handle(es, type, e.data)
      })
    }
    es.addEventListener('session', () => {
      this.stop()
      this.handlers.onSessionEnded?.()
    })
    es.onerror = () => {
      if (es !== this.es) return
      this.set({ ...this.state, connection: 'reconnecting' })
      if (es.readyState !== EventSource.CLOSED) return // the browser retries by itself
      es.close()
      this.handlers.onRefused?.()
      this.retryLater()
    }
  }

  private retryLater() {
    if (!this.running) return
    const delay = Math.min(30_000, 1000 * 2 ** this.failures)
    this.failures++
    clearTimeout(this.timer)
    this.timer = setTimeout(() => {
      if (this.running) this.connect()
    }, delay)
  }

  private handle(es: EventSource, type: string, data: string) {
    if (es !== this.es) return
    try {
      this.set(applyEvent(this.state, type, data))
    } catch (err) {
      // An envelope that does not validate: start over from a fresh snapshot rather than show a wrong state.
      console.warn('live: unreadable event, reconnecting', type, err)
      es.close()
      this.retryLater()
      return
    }
    if (type === 'snapshot') void this.loadHistory()
  }

  // The history fills gaps the stream cannot: the hour before the page opened, and any time it was disconnected.
  private async loadHistory() {
    try {
      const h = await request('GET', this.historyUrl, historySchema)
      if (this.running)
        this.set({ ...this.state, samples: mergeSamples(this.state.samples, h.samples) })
    } catch {
      // The charts keep what the stream delivers; the next snapshot tries again.
    }
  }
}
