import type { ListKind } from '@/live/store'

import { formatBytes, formatSince, shortId } from './format'

// How the connections page presents each protocol's lists: which columns, in what order. Items are shown through
// a generic accessor, so a field MediaMTX adds later appears in the detail view without code changes.

export type Item = Record<string, unknown>

export interface Column {
  label: string
  cell: (item: Item, now: number) => string
  numeric?: boolean
}

const str = (v: unknown) => (typeof v === 'string' && v !== '' ? v : '–')
const num = (v: unknown) => (typeof v === 'number' ? v : undefined)

const col = {
  id: { label: 'ID', cell: (it: Item) => shortId(typeof it.id === 'string' ? it.id : undefined) },
  remote: { label: 'Client', cell: (it: Item) => str(it.remoteAddr) },
  path: { label: 'Path', cell: (it: Item) => str(it.path) },
  state: { label: 'State', cell: (it: Item) => str(it.state) },
  user: { label: 'User', cell: (it: Item) => str(it.user) },
  transport: { label: 'Transport', cell: (it: Item) => str(it.transport) },
  created: {
    label: 'Since',
    cell: (it: Item, now: number) =>
      formatSince(typeof it.created === 'string' ? it.created : null, now),
  },
  inBytes: {
    label: 'Received',
    numeric: true,
    cell: (it: Item) => formatBytes(num(it.inboundBytes)),
  },
  outBytes: {
    label: 'Sent',
    numeric: true,
    cell: (it: Item) => formatBytes(num(it.outboundBytes)),
  },
  rtt: {
    label: 'RTT',
    numeric: true,
    cell: (it: Item) => (typeof it.msRTT === 'number' ? `${it.msRTT.toFixed(0)} ms` : '–'),
  },
  peer: {
    label: 'Peer connection',
    cell: (it: Item) => (it.peerConnectionEstablished === true ? 'established' : 'pending'),
  },
  lastRequest: {
    label: 'Last request',
    cell: (it: Item, now: number) =>
      formatSince(typeof it.lastRequest === 'string' ? it.lastRequest : null, now),
  },
} satisfies Record<string, Column>

export interface ListSpec {
  kind: ListKind
  title: string
  columns: Column[]
}

export interface Protocol {
  id: string
  label: string
  lists: ListSpec[]
}

const sessionCols = [
  col.id,
  col.remote,
  col.path,
  col.state,
  col.user,
  col.created,
  col.inBytes,
  col.outBytes,
]
const connCols = [col.id, col.remote, col.created, col.inBytes, col.outBytes]

export const protocols: Protocol[] = [
  {
    id: 'rtsp',
    label: 'RTSP',
    lists: [
      {
        kind: 'rtspSessions',
        title: 'Sessions',
        columns: [...sessionCols.slice(0, 4), col.transport, ...sessionCols.slice(4)],
      },
      { kind: 'rtspConns', title: 'Connections', columns: connCols },
    ],
  },
  {
    id: 'rtsps',
    label: 'RTSPS',
    lists: [
      {
        kind: 'rtspsSessions',
        title: 'Sessions',
        columns: [...sessionCols.slice(0, 4), col.transport, ...sessionCols.slice(4)],
      },
      { kind: 'rtspsConns', title: 'Connections', columns: connCols },
    ],
  },
  {
    id: 'rtmp',
    label: 'RTMP',
    lists: [{ kind: 'rtmpConns', title: 'Connections', columns: sessionCols }],
  },
  {
    id: 'rtmps',
    label: 'RTMPS',
    lists: [{ kind: 'rtmpsConns', title: 'Connections', columns: sessionCols }],
  },
  {
    id: 'srt',
    label: 'SRT',
    lists: [
      {
        kind: 'srtConns',
        title: 'Connections',
        columns: [...sessionCols.slice(0, 6), col.rtt, col.inBytes, col.outBytes],
      },
    ],
  },
  {
    id: 'webrtc',
    label: 'WebRTC',
    lists: [
      {
        kind: 'webrtcSessions',
        title: 'Sessions',
        columns: [...sessionCols.slice(0, 4), col.peer, ...sessionCols.slice(4)],
      },
    ],
  },
  {
    id: 'hls',
    label: 'HLS',
    lists: [
      {
        kind: 'hlsMuxers',
        title: 'Muxers',
        columns: [col.path, col.created, col.lastRequest, col.outBytes],
      },
      {
        kind: 'hlsSessions',
        title: 'Sessions',
        columns: [col.id, col.remote, col.path, col.user, col.created, col.outBytes],
      },
    ],
  },
  {
    id: 'moq',
    label: 'MoQ',
    lists: [
      {
        kind: 'moqSessions',
        title: 'Sessions',
        columns: [
          ...sessionCols.slice(0, 4),
          col.transport,
          col.created,
          col.inBytes,
          col.outBytes,
        ],
      },
    ],
  },
]

export function protocolById(id: string): Protocol | undefined {
  return protocols.find((p) => p.id === id)
}

/** Which protocol page lists a kind, for links from a path's readers and source. */
export function protocolOfKind(kind: ListKind): Protocol | undefined {
  return protocols.find((p) => p.lists.some((l) => l.kind === kind))
}

// A path's source and readers name their type (MediaMTX's PathSourceType and PathReaderType); these map the
// session and connection types to the list that holds them.
const typeToKind: Record<string, ListKind> = {
  rtspConn: 'rtspConns',
  rtspsConn: 'rtspsConns',
  hlsSession: 'hlsSessions',
  rtspSession: 'rtspSessions',
  rtspsSession: 'rtspsSessions',
  rtmpConn: 'rtmpConns',
  rtmpsConn: 'rtmpsConns',
  srtConn: 'srtConns',
  webRTCSession: 'webrtcSessions',
  hlsMuxer: 'hlsMuxers',
  moqSession: 'moqSessions',
}

export function kindOfType(type: string | undefined): ListKind | undefined {
  return type ? typeToKind[type] : undefined
}

/** A readable name for MediaMTX's source and reader types. */
export function describeType(type: string | undefined): string {
  if (!type) return '–'
  const names: Record<string, string> = {
    hlsSource: 'HLS source',
    redirect: 'Redirect',
    rpiCameraSource: 'Raspberry Pi camera',
    rtmpConn: 'RTMP publisher',
    rtmpsConn: 'RTMPS publisher',
    rtmpSource: 'RTMP source',
    rtspSession: 'RTSP session',
    rtspSource: 'RTSP source',
    rtspsSession: 'RTSPS session',
    srtConn: 'SRT connection',
    srtSource: 'SRT source',
    mpegtsSource: 'MPEG-TS source',
    rtpSource: 'RTP source',
    webRTCSession: 'WebRTC session',
    webRTCSource: 'WebRTC source',
    moqSource: 'MoQ source',
    moqSession: 'MoQ session',
    hlsMuxer: 'HLS muxer',
    hlsSession: 'HLS session',
    rtspConn: 'RTSP connection',
    rtspsConn: 'RTSPS connection',
    hidden: 'Internal reader',
  }
  return names[type] ?? type
}
