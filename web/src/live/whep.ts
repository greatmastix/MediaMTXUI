import { csrfTokenValue as csrfToken } from '@/api/client'

// A minimal WHEP client (RFC 9725) for the sidecar's proxy: receive-only, one audio and one video transceiver, the
// offer sent once ICE gathering is complete (MediaMTX does not need trickle ICE), the session ended with DELETE.

export interface WHEPSession {
  pc: RTCPeerConnection
  close: () => void
}

/** The ICE servers MediaMTX announces in Link headers (rel="ice-server"), for the peer connection's next start. */
export function iceServersFromLinks(links: string | null): RTCIceServer[] {
  if (!links) return []
  const out: RTCIceServer[] = []
  for (const part of links.split(/,(?=\s*<)/)) {
    const m = /<([^>]+)>\s*;(.*)$/.exec(part.trim())
    if (!m?.[1] || !(m[2] ?? '').includes('rel="ice-server"')) continue
    const params = m[2] ?? ''
    const user = /username="([^"]*)"/.exec(params)?.[1]
    const cred = /credential="([^"]*)"/.exec(params)?.[1]
    out.push({
      urls: m[1],
      ...(user ? { username: user } : {}),
      ...(cred ? { credential: cred } : {}),
    })
  }
  return out
}

function gathered(pc: RTCPeerConnection, ms: number): Promise<void> {
  return new Promise((resolve) => {
    if (pc.iceGatheringState === 'complete') {
      resolve()
      return
    }
    const done = () => {
      clearTimeout(t)
      pc.removeEventListener('icegatheringstatechange', check)
      resolve()
    }
    const check = () => {
      if (pc.iceGatheringState === 'complete') done()
    }
    const t = setTimeout(done, ms) // enough candidates by now: go with what there is
    pc.addEventListener('icegatheringstatechange', check)
  })
}

/**
 * Starts receiving path into video. policy "relay" forces a relay-only connection, which fails without TURN: the
 * player's way to test its HLS fallback. public uses the public WHEP endpoint (a public stream's watch link: no
 * session, no cookie, no key).
 */
export async function startWHEP(
  path: string,
  video: HTMLVideoElement,
  opts: { policy?: RTCIceTransportPolicy; signal?: AbortSignal; public?: boolean } = {},
): Promise<WHEPSession> {
  const auth: RequestInit = opts.public
    ? { credentials: 'omit' }
    : { credentials: 'same-origin', headers: { 'X-CSRF-Token': csrfToken() } }
  const pc = new RTCPeerConnection({ iceTransportPolicy: opts.policy ?? 'all' })
  pc.addTransceiver('video', { direction: 'recvonly' })
  pc.addTransceiver('audio', { direction: 'recvonly' })
  const stream = new MediaStream()
  pc.ontrack = (e) => {
    stream.addTrack(e.track)
    video.srcObject = stream
  }
  let sessionURL: string | null = null
  let closed = false
  const close = () => {
    if (closed) return
    closed = true
    window.removeEventListener('pagehide', close)
    // DELETE first: closing the peer connection makes MediaMTX end the session by itself, and a DELETE that arrives
    // after that finds nothing (404). keepalive: the request outlives the page when this runs on unload.
    const ended = sessionURL
      ? fetch(sessionURL, { ...auth, method: 'DELETE', keepalive: true }).catch(() => undefined)
      : Promise.resolve()
    void ended.finally(() => {
      pc.close()
    })
    if (document.visibilityState === 'hidden') pc.close() // unloading: no time to wait for the answer
  }
  // A reload or a closed tab does not run React's cleanup: end the session in MediaMTX anyway.
  window.addEventListener('pagehide', close)
  try {
    await pc.setLocalDescription(await pc.createOffer())
    await gathered(pc, 1500)
    const encoded = path.split('/').map(encodeURIComponent).join('/')
    const res = await fetch(opts.public ? `/whep/${encoded}` : `/api/v1/live/whep/${encoded}`, {
      ...auth,
      method: 'POST',
      headers: {
        ...(auth.headers as Record<string, string> | undefined),
        'Content-Type': 'application/sdp',
      },
      body: pc.localDescription?.sdp ?? '',
      signal: opts.signal,
    })
    if (res.status !== 201) throw new Error(`WHEP offer refused (${String(res.status)})`)
    sessionURL = res.headers.get('Location')
    // The player may have gone while the offer was on its way: end the session MediaMTX just created.
    if (opts.signal?.aborted) throw new Error('stopped')
    await pc.setRemoteDescription({ type: 'answer', sdp: await res.text() })
  } catch (err) {
    close()
    throw err
  }
  return { pc, close }
}
