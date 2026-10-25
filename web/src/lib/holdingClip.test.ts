import { describe, expect, it } from 'vitest'

import { videoBitrate } from './holdingClip'

describe('videoBitrate', () => {
  it("keeps the format's bitrate while the clip fits the server's limit, and lowers it where not", () => {
    expect(videoBitrate('1080p60', 3600)).toBe(5_000_000) // the server said no limit
    expect(videoBitrate('1080p60', 3600, 3.9 * 2 ** 30)).toBe(5_000_000) // an hour of 1080p60 is 2.25 GB
    const half = videoBitrate('1080p60', 3600, 0.5 * 2 ** 30) // 512 MiB for an hour
    expect(half).toBeLessThan(5_000_000)
    expect(((half + 128_000) * 3600) / 8).toBeLessThan(0.5 * 2 ** 30)
    expect(videoBitrate('720p50', 3600, 10 * 2 ** 20)).toBe(250_000) // never below a watchable floor
  })
})
