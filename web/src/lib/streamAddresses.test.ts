import { describe, expect, it } from 'vitest'

import type { Stream } from '@/api/streams'

import {
  mask,
  playbackTargets,
  publicTargets,
  publishTargets,
  rtmpServerAndKey,
  watchLink,
} from './streamAddresses'

const stream = (name: string, ingest: Partial<Stream['ingest']> = {}): Stream => ({
  id: 1,
  name,
  title: name,
  target: '',
  audio: 'aac',
  holding: '',
  clips: { aac: false, opus: false },
  format: '1080p50',
  maxReaders: 0,
  record: false,
  public: true,
  owner: null,
  keys: {},
  createdAt: '',
  createdBy: '',
  canManage: true,
  canAdmin: false,
  ingest: {
    host: 'mtx.example',
    rtmp: 1935,
    srt: 8890,
    rtsp: 8554,
    hls: true,
    webrtc: true,
    ...ingest,
  },
})
const key = { kind: 'publish' as const, name: 'key-abc', secret: 's3cr3t' }

describe('stream addresses', () => {
  it('splits RTMP into server and key the way OBS joins them', () => {
    expect(rtmpServerAndKey(stream('live/alice'), key)).toEqual({
      server: 'rtmp://mtx.example/live',
      key: 'alice?user=key-abc&pass=s3cr3t',
    })
    expect(rtmpServerAndKey(stream('alice', { rtmp: 1936 }), key)).toEqual({
      server: 'rtmp://mtx.example:1936',
      key: 'alice?user=key-abc&pass=s3cr3t',
    })
  })

  it('lists every protocol that is on, with the credential where each expects it', () => {
    const t = publishTargets(stream('live/alice', { srt: undefined }), key, 'https://ui.example')
    expect(t.map((x) => x.protocol)).toEqual(['RTMP', 'WHIP', 'RTSP'])
    expect(t[1]?.fields).toEqual([
      { label: 'Server', value: 'https://ui.example/whip/live/alice' },
      { label: 'Bearer token', value: 'key-abc:s3cr3t', secret: true },
    ])
    expect(t[2]?.fields[0]?.value).toBe('rtsp://key-abc:s3cr3t@mtx.example:8554/live/alice')
    const srt = publishTargets(stream('live/alice'), key, 'https://ui.example')[1]
    expect(srt?.fields[0]?.value).toBe(
      'srt://mtx.example:8890?streamid=publish:live/alice:key-abc:s3cr3t',
    )
  })

  it('lists where players read it, with the playback key', () => {
    const view = { kind: 'playback' as const, name: 'view-x', secret: 'r3ad' }
    const t = playbackTargets(stream('live/alice'), view, 'https://ui.example')
    expect(t.map((x) => x.protocol)).toEqual(['RTSP', 'RTMP', 'SRT', 'WHEP'])
    expect(t[0]?.fields[0]?.value).toBe('rtsp://view-x:r3ad@mtx.example:8554/live/alice')
    expect(t[1]?.fields[0]?.value).toBe('rtmp://mtx.example/live/alice?user=view-x&pass=r3ad')
    expect(t[2]?.fields[0]?.value).toBe(
      'srt://mtx.example:8890?streamid=read:live/alice:view-x:r3ad',
    )
  })

  it('gives public streams keyless addresses and a watch link', () => {
    const s = stream('live/al ice')
    expect(watchLink(s, 'https://ui.example')).toBe('https://ui.example/s/live/al%20ice')
    const t = publicTargets(stream('live/alice'), 'https://ui.example')
    expect(t.map((x) => x.fields[0]?.value)).toEqual([
      'rtsp://mtx.example:8554/live/alice',
      'rtmp://mtx.example/live/alice',
      'srt://mtx.example:8890?streamid=read:live/alice',
      'https://ui.example/whep/live/alice',
    ])
  })

  it('masks secrets', () => {
    expect(mask('a?pass=s3cr3t', 's3cr3t')).toBe('a?pass=••••••••••••')
  })
})
