import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { adminSession, fakeSidecar } from '@/test/fakeSidecar'
import { FakeEventSource, installFakeEventSource, snapshot } from '@/test/fakeEventSource'

import { LiveClient } from './client'

describe('LiveClient', () => {
  beforeEach(() => {
    installFakeEventSource()
    fakeSidecar({ session: adminSession })
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  it('follows the stream and loads the history with the snapshot', async () => {
    const c = new LiveClient()
    const seen = vi.fn()
    c.subscribe(seen)
    c.start()
    expect(FakeEventSource.latest.url).toBe('/api/v1/events')
    expect(c.getState().connection).toBe('connecting')
    FakeEventSource.latest.open()
    FakeEventSource.latest.emit('snapshot', snapshot({ paths: { items: [{ name: 'cam' }] } }))
    expect(c.getState().connection).toBe('open')
    expect(c.getState().lists.paths?.items.has('cam')).toBe(true)
    await vi.waitFor(() => {
      expect(c.getState().samples).toHaveLength(2)
    })
    expect(seen).toHaveBeenCalled()
    c.stop()
    expect(FakeEventSource.latest.readyState).toBe(FakeEventSource.CLOSED)
  })

  it('leaves dropped streams to the browser, and retries refused ones with backoff', () => {
    vi.useFakeTimers()
    const refused = vi.fn()
    const c = new LiveClient()
    c.setHandlers({ onRefused: refused })
    c.start()
    FakeEventSource.latest.open()
    FakeEventSource.latest.fail(false)
    expect(c.getState().connection).toBe('reconnecting')
    expect(FakeEventSource.instances).toHaveLength(1)
    expect(refused).not.toHaveBeenCalled()

    FakeEventSource.latest.fail(true)
    expect(refused).toHaveBeenCalledOnce()
    vi.advanceTimersByTime(999)
    expect(FakeEventSource.instances).toHaveLength(1)
    vi.advanceTimersByTime(1)
    expect(FakeEventSource.instances).toHaveLength(2)
    FakeEventSource.latest.fail(true)
    vi.advanceTimersByTime(1999)
    expect(FakeEventSource.instances).toHaveLength(2)
    vi.advanceTimersByTime(1)
    expect(FakeEventSource.instances).toHaveLength(3)
    c.stop()
    FakeEventSource.latest.fail(true)
    vi.advanceTimersByTime(60_000)
    expect(FakeEventSource.instances).toHaveLength(3)
  })

  it('reports the end of the session and stops', () => {
    const ended = vi.fn()
    const c = new LiveClient()
    c.setHandlers({ onSessionEnded: ended })
    c.start()
    FakeEventSource.latest.emit('session', { state: 'ended' })
    expect(ended).toHaveBeenCalledOnce()
    expect(FakeEventSource.latest.readyState).toBe(FakeEventSource.CLOSED)
  })

  it('starts over when an event does not validate', () => {
    vi.useFakeTimers()
    vi.spyOn(console, 'warn').mockImplementation(() => undefined)
    const c = new LiveClient()
    c.start()
    FakeEventSource.latest.emit('update', { nonsense: true })
    expect(FakeEventSource.latest.readyState).toBe(FakeEventSource.CLOSED)
    vi.advanceTimersByTime(1000)
    expect(FakeEventSource.instances).toHaveLength(2)
    c.stop()
  })
})
