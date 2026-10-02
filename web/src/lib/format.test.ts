import { describe, expect, it } from 'vitest'

import {
  formatBitrate,
  formatBytes,
  formatDuration,
  formatSince,
  niceCeiling,
  shortId,
} from './format'

describe('format', () => {
  it('formats bitrates in decimal units', () => {
    expect(formatBitrate(0)).toBe('0 bit/s')
    expect(formatBitrate(999)).toBe('999 bit/s')
    expect(formatBitrate(1500)).toBe('1.50 kbit/s')
    expect(formatBitrate(1_536_000)).toBe('1.54 Mbit/s')
    expect(formatBitrate(250_000_000)).toBe('250 Mbit/s')
    expect(formatBitrate(null)).toBe('–')
  })

  it('formats byte counts in binary units', () => {
    expect(formatBytes(512)).toBe('512 B')
    expect(formatBytes(1536)).toBe('1.50 KiB')
    expect(formatBytes(10 * 1024 ** 3)).toBe('10.0 GiB')
    expect(formatBytes(undefined)).toBe('–')
  })

  it('formats durations and relative times', () => {
    expect(formatDuration(12)).toBe('12s')
    expect(formatDuration(250)).toBe('4m 10s')
    expect(formatDuration(7500)).toBe('2h 5m')
    expect(formatDuration(3 * 86400 + 4 * 3600)).toBe('3d 4h')
    expect(formatDuration(-5)).toBe('0s')
    expect(formatSince('2026-09-29T10:00:00Z', Date.parse('2026-09-29T10:05:03Z'))).toBe(
      '5m 3s ago',
    )
    expect(formatSince('garbage', 0)).toBe('–')
  })

  it('rounds chart scales up', () => {
    expect(niceCeiling(0)).toBe(1)
    expect(niceCeiling(3)).toBe(5)
    expect(niceCeiling(1_200_000)).toBe(2_000_000)
    expect(niceCeiling(6)).toBe(10)
    expect(shortId('0123456789abcdef')).toBe('01234567')
  })
})
