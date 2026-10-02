import type { Lists, ListKind } from '@/live/store'

// What saving a change interrupts. On a reload MediaMTX closes and reopens only the servers whose settings changed
// (verified for single-protocol changes); their clients drop and have to reconnect. Settings every server
// uses (timeouts, queue sizes, the connect hooks) restart all of them. The mapping follows the setting names; it is
// a warning, not a promise.

interface Server {
  label: string
  owns: (key: string) => boolean
  clients: ListKind[]
}

const servers: Server[] = [
  {
    label: 'RTSP',
    owns: (k) => /^(rtsp|rtp|rtcp|multicast|srtp|srtcp)/.test(k),
    clients: ['rtspSessions', 'rtspsSessions'],
  },
  { label: 'RTMP', owns: (k) => k.startsWith('rtmp'), clients: ['rtmpConns', 'rtmpsConns'] },
  { label: 'HLS', owns: (k) => k.startsWith('hls'), clients: ['hlsSessions'] },
  { label: 'WebRTC', owns: (k) => k.startsWith('webrtc'), clients: ['webrtcSessions'] },
  { label: 'SRT', owns: (k) => k === 'srt' || /^srt[A-Z]/.test(k), clients: ['srtConns'] },
  { label: 'MoQ', owns: (k) => k.startsWith('moq'), clients: ['moqSessions'] },
]

const everyServer = new Set([
  'readTimeout',
  'writeTimeout',
  'writeQueueSize',
  'udpMaxPayloadSize',
  'udpReadBufferSize',
  'runOnConnect',
  'runOnConnectRestart',
  'runOnDisconnect',
])

const count = (lists: Lists, kinds: ListKind[]) =>
  kinds.reduce((n, k) => n + (lists[k]?.available ? lists[k].items.size : 0), 0)

const plural = (n: number, one: string, many: string) => `${String(n)} ${n === 1 ? one : many}`

/** A sentence about what saving these global settings interrupts, or null when it interrupts nothing. */
export function globalImpact(changed: readonly string[], lists: Lists): string | null {
  const hit = changed.some((k) => everyServer.has(k))
    ? servers
    : servers.filter((s) => changed.some((k) => s.owns(k)))
  if (hit.length === 0) return null
  const clients = hit.reduce((n, s) => n + count(lists, s.clients), 0)
  const names = hit.map((s) => s.label).join(', ')
  const what = hit.length === 1 ? `the ${names} server` : `the ${names} servers`
  return clients === 0
    ? `Saving restarts ${what}; nobody is connected to ${hit.length === 1 ? 'it' : 'them'} now.`
    : `Saving restarts ${what}: ${plural(clients, 'client', 'clients')} will drop and have to reconnect.`
}

/** The same for one path: MediaMTX recreates a path whose settings changed. */
export function pathImpact(name: string, lists: Lists): string | null {
  const p = lists.paths?.items.get(name)
  if (!p?.online) return null
  const readers = p.readers?.length ?? 0
  return `Saving restarts path ${name}: its source and ${plural(readers, 'reader', 'readers')} will reconnect.`
}
