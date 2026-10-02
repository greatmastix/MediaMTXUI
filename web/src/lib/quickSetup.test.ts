import { describe, expect, it } from 'vitest'

import { enabled, portOf, protocols, publishAddress, readAddresses } from './quickSetup'

describe('quick setup addresses', () => {
  it('follows the enabled protocols and their ports', () => {
    const global = { rtsp: true, rtmp: false, srt: true, srtAddress: '0.0.0.0:9000' }
    expect(enabled(global, 'rtmp')).toBe(false)
    expect(enabled({}, 'rtmp')).toBe(true) // MediaMTX's default
    const srt = protocols.find((p) => p.key === 'srt')
    expect(srt && portOf(global, srt)).toBe(9000)
    expect(readAddresses('mtx.example.com', global, 'cam1')).toEqual([
      { protocol: 'RTSP', url: 'rtsp://mtx.example.com:8554/cam1' },
      { protocol: 'SRT', url: 'srt://mtx.example.com:9000?streamid=read:cam1' },
    ])
    expect(publishAddress('h', {}, 'live/obs', 'rtmp')).toBe('rtmp://h/live/obs')
    expect(publishAddress('h', { rtmpAddress: ':1936' }, 'x', 'rtmp')).toBe('rtmp://h:1936/x')
    expect(publishAddress('h', {}, 'x', 'srt')).toBe('srt://h:8890?streamid=publish:x')
  })
})

describe('credential addresses', () => {
  it('puts the credential where each protocol expects it', async () => {
    const { credentialAddresses } = await import('./quickSetup')
    expect(credentialAddresses('h', { rtmp: true }, 'cam1', 'obs', 's3cr3t', 'publish')).toEqual([
      { protocol: 'RTSP', url: 'rtsp://obs:s3cr3t@h:8554/cam1' },
      { protocol: 'RTMP', url: 'rtmp://h/cam1?user=obs&pass=s3cr3t' },
      { protocol: 'SRT', url: 'srt://h:8890?streamid=publish:cam1:obs:s3cr3t' },
    ])
  })
})

describe('WebRTC addresses', () => {
  it('names WHIP for publishing and WHEP for reading, on the UI origin', async () => {
    const { rtcAddress } = await import('./quickSetup')
    expect(rtcAddress('https://ui.example', {}, 'live/obs', 'publish')).toEqual({
      protocol: 'WHIP',
      url: 'https://ui.example/whip/live/obs',
    })
    expect(rtcAddress('https://ui.example', {}, 'a b', 'read')?.url).toBe(
      'https://ui.example/whep/a%20b',
    )
    expect(rtcAddress('https://ui.example', {}, '<path>', 'read')?.url).toBe(
      'https://ui.example/whep/<path>',
    )
    expect(rtcAddress('https://ui.example', { webrtc: false }, 'x', 'read')).toBeNull()
  })
})
