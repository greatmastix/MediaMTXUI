// Helpers for the quick setup: which protocols MediaMTX serves, and the addresses clients use to publish and read.
// MediaMTX remuxes: a stream that comes in over any protocol can be read over every protocol that is switched on.

export type Values = Record<string, unknown>

export interface Protocol {
  key: 'rtsp' | 'rtmp' | 'srt' | 'hls' | 'webrtc'
  label: string
  /** The setting with the listener's address, and MediaMTX's default for it. */
  address: string
  port: number
  /** Whether clients reach it on a stream port of their own (HLS and WebRTC signalling go through the UI's host). */
  direct: boolean
  about: string
}

export const protocols: Protocol[] = [
  {
    key: 'rtsp',
    label: 'RTSP',
    address: 'rtspAddress',
    port: 8554,
    direct: true,
    about: 'Cameras, VLC, ffmpeg and most NVRs.',
  },
  {
    key: 'rtmp',
    label: 'RTMP',
    address: 'rtmpAddress',
    port: 1935,
    direct: true,
    about: 'OBS and most streaming software; the classic way to publish.',
  },
  {
    key: 'srt',
    label: 'SRT',
    address: 'srtAddress',
    port: 8890,
    direct: true,
    about: 'Reliable streaming over lossy or long-distance links.',
  },
  {
    key: 'hls',
    label: 'HLS',
    address: 'hlsAddress',
    port: 8888,
    direct: false,
    about: 'Playback in any browser and on phones, with a few seconds of delay.',
  },
  {
    key: 'webrtc',
    label: 'WebRTC',
    address: 'webrtcAddress',
    port: 8889,
    direct: false,
    about: 'Playback in the browser with well under a second of delay.',
  },
]

/** Whether a protocol server is on: MediaMTX turns every one on unless the file says no. */
export function enabled(global: Values, key: Protocol['key']): boolean {
  const v = global[key]
  return v === undefined || v === null ? true : v === true
}

/** The port a listener setting names (":8554", "0.0.0.0:8554"), or the protocol's default. */
export function portOf(global: Values, p: Protocol): number {
  const addr = global[p.address]
  if (typeof addr !== 'string') return p.port
  const m = /:(\d+)$/.exec(addr)
  return m ? Number(m[1]) : p.port
}

export interface Address {
  protocol: string
  url: string
}

function address(
  host: string,
  global: Values,
  p: Protocol,
  name: string,
  mode: 'read' | 'publish',
): string {
  const port = portOf(global, p)
  switch (p.key) {
    case 'rtsp':
      return `rtsp://${host}:${String(port)}/${name}`
    case 'rtmp':
      return `rtmp://${host}${port === 1935 ? '' : `:${String(port)}`}/${name}`
    case 'srt':
      return `srt://${host}:${String(port)}?streamid=${mode}:${name}`
    default:
      return ''
  }
}

/** Where readers play a path, for every protocol that is on and reachable directly. */
export function readAddresses(host: string, global: Values, name: string): Address[] {
  return protocols
    .filter((p) => p.direct && enabled(global, p.key))
    .map((p) => ({ protocol: p.label, url: address(host, global, p, name, 'read') }))
}

/** Where a publisher sends a path over one protocol. */
export function publishAddress(
  host: string,
  global: Values,
  name: string,
  key: Protocol['key'],
): string {
  const p = protocols.find((x) => x.key === key)
  return p ? address(host, global, p, name, 'publish') : ''
}

/** Retention choices for recordings, as MediaMTX durations. */
export const retention = [
  { label: 'Keep for a day', value: '24h' },
  { label: 'Keep for a week', value: '168h' },
  { label: 'Keep for 30 days', value: '720h' },
  { label: 'Keep until deleted', value: '0s' },
] as const

/**
 * Client addresses with a credential in them, the way MediaMTX takes credentials per protocol: RTSP in the URL's user
 * part, RTMP as query parameters, SRT in the stream id. Secrets in URLs are fine here: these go to the client's own
 * configuration, never through the reverse proxy's logs.
 */
export function credentialAddresses(
  host: string,
  global: Values,
  path: string,
  name: string,
  secret: string,
  action: 'read' | 'publish',
): Address[] {
  const out: Address[] = []
  const u = encodeURIComponent(name)
  const s = encodeURIComponent(secret)
  for (const p of protocols.filter((x) => x.direct && enabled(global, x.key))) {
    const port = portOf(global, p)
    if (p.key === 'rtsp')
      out.push({ protocol: 'RTSP', url: `rtsp://${u}:${s}@${host}:${String(port)}/${path}` })
    if (p.key === 'rtmp') {
      const hostPort = port === 1935 ? host : `${host}:${String(port)}`
      out.push({ protocol: 'RTMP', url: `rtmp://${hostPort}/${path}?user=${u}&pass=${s}` })
    }
    if (p.key === 'srt') {
      out.push({
        protocol: 'SRT',
        url: `srt://${host}:${String(port)}?streamid=${action}:${path}:${name}:${secret}`,
      })
    }
  }
  return out
}

/**
 * The WHIP (publish) or WHEP (read) address for WebRTC clients such as OBS, served by the sidecar on the UI's own origin.
 * It carries no secret: the client sends the credential in the Authorization header (Bearer for a token, Basic for a
 * name and secret), which the reverse proxy does not log. Null when MediaMTX's WebRTC server is off.
 */
export function rtcAddress(
  origin: string,
  global: Values,
  path: string,
  action: 'read' | 'publish',
): Address | null {
  if (!enabled(global, 'webrtc')) return null
  const kind = action === 'publish' ? 'whip' : 'whep'
  const p = path
    .split('/')
    .map((x) => (x === '<path>' ? x : encodeURIComponent(x)))
    .join('/')
  return { protocol: kind.toUpperCase(), url: `${origin}/${kind}/${p}` }
}
