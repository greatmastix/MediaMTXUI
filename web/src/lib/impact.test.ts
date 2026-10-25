import { describe, expect, it } from 'vitest'

import { applyEvent, initialState } from '@/live/store'
import { snapshot } from '@/test/fakeEventSource'

import { debugLogWarning, globalImpact, logLevelImpact, pathImpact } from './impact'

const lists = applyEvent(
  initialState,
  'snapshot',
  JSON.stringify(
    snapshot({
      paths: {
        items: [{ name: 'cam', online: true, readers: [{ type: 'rtspSession', id: '1' }] }],
      },
      rtspSessions: { items: [{ id: '1' }, { id: '2' }] },
      rtmpConns: { items: [{ id: '3' }] },
      srtConns: { available: false },
    }),
  ),
).lists

describe('impact', () => {
  it('names the servers a change restarts and the clients they drop', () => {
    expect(globalImpact(['logStructured'], lists)).toBeNull()
    expect(globalImpact(['logLevel'], lists)).toBe(
      'Saving restarts the RTSP, RTMP, HLS, WebRTC, SRT, MoQ servers: 3 clients will drop and have to reconnect.',
    )
    expect(globalImpact(['rtmpAddress'], lists)).toBe(
      'Saving restarts the RTMP server: 1 client will drop and have to reconnect.',
    )
    expect(globalImpact(['rtspTransports', 'srtpAddress'], lists)).toBe(
      'Saving restarts the RTSP server: 2 clients will drop and have to reconnect.',
    )
    expect(globalImpact(['srtAddress'], lists)).toBe(
      'Saving restarts the SRT server; nobody is connected to it now.',
    )
    expect(globalImpact(['readTimeout'], lists)).toMatch(
      /^Saving restarts the RTSP, RTMP, HLS, WebRTC, SRT, MoQ servers: 3 clients/,
    )
  })

  it('warns when logLevel goes to debug', () => {
    expect(logLevelImpact({ logLevel: 'debug' })).toBe(debugLogWarning)
    expect(logLevelImpact({ logLevel: 'info' })).toBeNull()
    expect(logLevelImpact({ readTimeout: '15s' })).toBeNull()
  })

  it('warns about online paths only', () => {
    expect(pathImpact('cam', lists)).toBe(
      'Saving restarts path cam: its source and 1 reader will reconnect.',
    )
    expect(pathImpact('other', lists)).toBeNull()
  })
})
