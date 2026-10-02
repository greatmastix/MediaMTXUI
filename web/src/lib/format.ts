// Number and time formatting for people. Units follow the IEC/SI conventions MediaMTX users expect: bitrates in
// decimal kbit/s, Mbit/s; byte counts in binary KiB, MiB.

const bitUnits = ['bit/s', 'kbit/s', 'Mbit/s', 'Gbit/s', 'Tbit/s']
const byteUnits = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB']

function scaled(value: number, base: number, units: readonly string[]) {
  let v = value
  let i = 0
  while (Math.abs(v) >= base && i < units.length - 1) {
    v /= base
    i++
  }
  const digits = i === 0 || Math.abs(v) >= 100 ? 0 : Math.abs(v) >= 10 ? 1 : 2
  return `${v.toFixed(digits)} ${units[i] ?? ''}`
}

export function formatBitrate(bps: number | null | undefined): string {
  if (bps == null || !Number.isFinite(bps)) return '–'
  return scaled(bps, 1000, bitUnits)
}

export function formatBytes(bytes: number | null | undefined): string {
  if (bytes == null || !Number.isFinite(bytes)) return '–'
  return scaled(bytes, 1024, byteUnits)
}

export function formatCount(n: number | null | undefined): string {
  return n == null ? '–' : n.toLocaleString('en')
}

/** A duration in seconds as "3d 4h", "2h 5m", "4m 10s" or "12s". */
export function formatDuration(seconds: number): string {
  const s = Math.max(0, Math.floor(seconds))
  const d = Math.floor(s / 86400)
  const h = Math.floor((s % 86400) / 3600)
  const m = Math.floor((s % 3600) / 60)
  const sec = s % 60
  if (d > 0) return `${d}d ${h}h`
  if (h > 0) return `${h}h ${m}m`
  if (m > 0) return `${m}m ${sec}s`
  return `${sec}s`
}

/** How long ago an ISO timestamp was, relative to now (milliseconds), e.g. "5m 3s ago". */
export function formatSince(iso: string | null | undefined, now: number): string {
  if (!iso) return '–'
  const t = Date.parse(iso)
  if (Number.isNaN(t)) return '–'
  return `${formatDuration((now - t) / 1000)} ago`
}

export function formatTime(iso: string | number | null | undefined): string {
  if (iso == null || iso === '') return '–'
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return '–'
  return d.toLocaleString('en-GB', { dateStyle: 'medium', timeStyle: 'medium' })
}

export function formatClock(ms: number): string {
  return new Date(ms).toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit' })
}

/** A day for chart axes: "3 Oct". */
export function formatDay(ms: number): string {
  return new Date(ms).toLocaleDateString('en-GB', { day: 'numeric', month: 'short' })
}

/** A day and time for chart read-outs: "3 Oct, 14:05". */
export function formatDayClock(ms: number): string {
  return `${formatDay(ms)}, ${formatClock(ms)}`
}

/** A UUID shortened for tables; the full id is in the detail view. */
export function shortId(id: string | undefined): string {
  return id ? id.slice(0, 8) : '–'
}

/** A round upper bound for a chart's y axis: 1, 2 or 5 times a power of ten. */
export function niceCeiling(v: number): number {
  if (v <= 0) return 1
  const p = 10 ** Math.floor(Math.log10(v))
  for (const m of [1, 2, 5]) if (v <= m * p) return m * p
  return 10 * p
}
