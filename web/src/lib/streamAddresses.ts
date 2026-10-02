import type { Stream, StreamKey } from '@/api/streams'

// What a streamer types into OBS (or another encoder) to publish, and what a player needs to read, for each protocol
// that is on. The key's secret is in these on purpose: they go into the client's own settings, straight to MediaMTX's
// stream ports, never through the reverse proxy and its logs.

export interface PublishTarget {
  protocol: 'RTMP' | 'SRT' | 'RTSP' | 'WHIP' | 'WHEP'
  /** One line on when to pick it. */
  use: string
  /** Fields as the encoder names them, in order. */
  fields: { label: string; value: string; secret?: boolean }[]
}

const enc = encodeURIComponent

function hostPort(host: string, port: number, dflt: number) {
  return port === dflt ? host : `${host}:${String(port)}`
}

/** OBS joins Server and Stream key with a slash: the last path segment goes into the key, with the credential. */
export function rtmpServerAndKey(stream: Stream, key: StreamKey): { server: string; key: string } {
  const port = stream.ingest.rtmp ?? 1935
  const cut = stream.name.lastIndexOf('/')
  const app = cut < 0 ? '' : `/${stream.name.slice(0, cut)}`
  const last = stream.name.slice(cut + 1)
  return {
    server: `rtmp://${hostPort(stream.ingest.host, port, 1935)}${app}`,
    key: `${last}?user=${enc(key.name)}&pass=${enc(key.secret)}`,
  }
}

export function publishTargets(stream: Stream, key: StreamKey, origin: string): PublishTarget[] {
  const { host, rtmp, srt, rtsp, webrtc } = stream.ingest
  const out: PublishTarget[] = []
  if (rtmp) {
    const r = rtmpServerAndKey(stream, key)
    out.push({
      protocol: 'RTMP',
      use: 'OBS, Streamlabs and most encoders: the usual choice.',
      fields: [
        { label: 'Server', value: r.server },
        { label: 'Stream key', value: r.key, secret: true },
      ],
    })
  }
  if (srt) {
    out.push({
      protocol: 'SRT',
      use: 'Unstable connections (mobile, long distance): recovers lost packets.',
      fields: [
        {
          label: 'URL',
          value: `srt://${host}:${String(srt)}?streamid=publish:${stream.name}:${key.name}:${key.secret}`,
          secret: true,
        },
      ],
    })
  }
  if (webrtc) {
    out.push({
      protocol: 'WHIP',
      use: 'OBS 30+ (service WHIP): lowest delay to WebRTC viewers.',
      fields: [
        { label: 'Server', value: `${origin}/whip/${stream.name.split('/').map(enc).join('/')}` },
        { label: 'Bearer token', value: `${key.name}:${key.secret}`, secret: true },
      ],
    })
  }
  if (rtsp) {
    out.push({
      protocol: 'RTSP',
      use: 'Cameras and ffmpeg.',
      fields: [
        {
          label: 'URL',
          value: `rtsp://${enc(key.name)}:${enc(key.secret)}@${host}:${String(rtsp)}/${stream.name}`,
          secret: true,
        },
      ],
    })
  }
  return out
}

/** Masks the secret parts of a value for display until revealed. */
export function mask(value: string, secret: string): string {
  return secret ? value.split(secret).join('•'.repeat(12)) : value
}

/** Where players and other servers read a stream, each with the playback key, for the protocols that are on. */
export function playbackTargets(stream: Stream, key: StreamKey, origin: string): PublishTarget[] {
  const { host, rtmp, srt, rtsp, webrtc } = stream.ingest
  const out: PublishTarget[] = []
  if (rtsp) {
    out.push({
      protocol: 'RTSP',
      use: 'VRChat on PC, VLC, and most players and cameras.',
      fields: [
        {
          label: 'URL',
          value: `rtsp://${enc(key.name)}:${enc(key.secret)}@${host}:${String(rtsp)}/${stream.name}`,
          secret: true,
        },
      ],
    })
  }
  if (rtmp) {
    out.push({
      protocol: 'RTMP',
      use: 'An OBS media source, or another server re-streaming it.',
      fields: [
        {
          label: 'URL',
          value: `rtmp://${hostPort(host, rtmp, 1935)}/${stream.name}?user=${enc(key.name)}&pass=${enc(key.secret)}`,
          secret: true,
        },
      ],
    })
  }
  if (srt) {
    out.push({
      protocol: 'SRT',
      use: 'Other encoders and servers over long or shaky links.',
      fields: [
        {
          label: 'URL',
          value: `srt://${host}:${String(srt)}?streamid=read:${stream.name}:${key.name}:${key.secret}`,
          secret: true,
        },
      ],
    })
  }
  if (webrtc) {
    out.push({
      protocol: 'WHEP',
      use: 'Web players and OBS (WHEP source): WebRTC, under a second behind.',
      fields: [
        { label: 'Server', value: `${origin}/whep/${stream.name.split('/').map(enc).join('/')}` },
        { label: 'Bearer token', value: `${key.name}:${key.secret}`, secret: true },
      ],
    })
  }
  return out
}

/** The watch link: a page anyone can open in a browser (public streams only). */
export function watchLink(stream: Stream, origin: string): string {
  return `${origin}/s/${stream.name.split('/').map(enc).join('/')}`
}

/** Keyless addresses of a public stream for players and servers. */
export function publicTargets(stream: Stream, origin: string): PublishTarget[] {
  const { host, rtmp, srt, rtsp, webrtc } = stream.ingest
  const out: PublishTarget[] = []
  if (rtsp) {
    out.push({
      protocol: 'RTSP',
      use: 'VRChat on PC, VLC, and most players.',
      fields: [{ label: 'URL', value: `rtsp://${host}:${String(rtsp)}/${stream.name}` }],
    })
  }
  if (rtmp) {
    out.push({
      protocol: 'RTMP',
      use: 'An OBS media source, or another server re-streaming it.',
      fields: [{ label: 'URL', value: `rtmp://${hostPort(host, rtmp, 1935)}/${stream.name}` }],
    })
  }
  if (srt) {
    out.push({
      protocol: 'SRT',
      use: 'Other encoders and servers over long or shaky links.',
      fields: [
        { label: 'URL', value: `srt://${host}:${String(srt)}?streamid=read:${stream.name}` },
      ],
    })
  }
  if (webrtc) {
    out.push({
      protocol: 'WHEP',
      use: 'Web players and OBS (WHEP source): WebRTC, under a second behind.',
      fields: [
        { label: 'Server', value: `${origin}/whep/${stream.name.split('/').map(enc).join('/')}` },
      ],
    })
  }
  return out
}
