import { describe, expect, it } from 'vitest'

import { healthChecks, targetById, targets } from './targets'

const h264 = (width: number, height: number) => ({ codec: 'H264', codecProps: { width, height } })
const aac = { codec: 'MPEG-4 Audio', codecProps: { sampleRate: 48000, channelCount: 2 } }
const byId = (checks: ReturnType<typeof healthChecks>) => new Map(checks.map((c) => [c.id, c]))

describe('targets', () => {
  it('have unique ids the sidecar accepts', () => {
    const ids = targets.map((t) => t.id)
    expect(new Set(ids).size).toBe(ids.length)
    for (const id of ids) expect(id).toMatch(/^[a-z0-9-]{1,32}$/)
  })
})

describe('healthChecks', () => {
  const twitch = targetById('twitch')

  it('passes a stream that fits its target', () => {
    const checks = healthChecks(twitch, [h264(1920, 1080), aac], [6e6, 6.1e6, 5.9e6])
    expect(checks.every((c) => c.ok)).toBe(true)
    expect([...byId(checks).keys()].sort()).toEqual([
      'audio',
      'bitrate',
      'codec',
      'resolution',
      'stability',
    ])
  })

  it('finds each problem', () => {
    const checks = byId(
      healthChecks(
        twitch,
        [{ codec: 'H265', codecProps: { width: 2560, height: 1440 } }],
        [12e6, 3e6, 12e6, 3e6],
      ),
    )
    expect(checks.get('bitrate')?.ok).toBe(false)
    expect(checks.get('stability')?.ok).toBe(false)
    expect(checks.get('audio')?.ok).toBe(false)
    expect(checks.get('codec')).toMatchObject({ ok: false, text: 'Twitch cannot play H265.' })
    expect(checks.get('resolution')?.ok).toBe(false)
  })

  it('flags AAC for low latency, where browsers need Opus', () => {
    const checks = byId(healthChecks(targetById('low-latency'), [h264(1280, 720), aac], []))
    expect(checks.get('codec')).toMatchObject({
      ok: false,
      text: 'Low latency cannot play MPEG-4 Audio.',
    })
  })

  it('runs only the target-free checks without a target, and no bitrate checks before there are samples', () => {
    expect([...byId(healthChecks(undefined, [h264(1280, 720), aac], [])).keys()]).toEqual(['audio'])
    expect([...byId(healthChecks(undefined, [h264(1280, 720)], [1e6, 1e6, 1e6])).keys()]).toEqual([
      'stability',
      'audio',
    ])
  })
})
