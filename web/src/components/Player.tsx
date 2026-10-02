import { useEffect, useRef, useState } from 'react'

import { formatBitrate } from '@/lib/format'
import { cn } from '@/lib/utils'
import { startWHEP, type WHEPSession } from '@/live/whep'

// Plays a path: WebRTC (WHEP) first, for the lowest delay; HLS when WebRTC does not connect in time (UDP blocked,
// no route to the media port) or connects but never brings video (MediaMTX closes WebRTC readers of H.264 with
// B-frames), with the reason on screen. Both go through the sidecar, which the session cookie authorizes. A session
// that ends after playing (the encoder takes over from the holding screen, the path restarts after a settings change,
// HLS gives up) starts again by itself. hls.js loads only when needed.

type Mode = 'connecting' | 'webrtc' | 'hls' | 'error'

const webrtcTimeout = 6000
const stallAfter = 5 // seconds without new video before a WebRTC session counts as ended
const retryAfter = 1500 // ms before starting again
const shortLived = 12_000 // ms: a WebRTC session that ends sooner did not really play

interface Stats {
  width: number
  height: number
  fps?: number
  bps?: number
}

export function Player({
  path,
  policy = 'all',
  stats: showStats = false,
  public: isPublic = false,
  className,
}: {
  path: string
  /** A public stream's watch link: public WHEP and HLS endpoints, no session. */
  public?: boolean
  /** "relay" makes WebRTC fail (no TURN server), to exercise the fallback. */
  policy?: RTCIceTransportPolicy
  stats?: boolean
  className?: string
}) {
  const video = useRef<HTMLVideoElement>(null)
  const [mode, setMode] = useState<Mode>('connecting')
  const [note, setNote] = useState<string | null>(null)
  const [stats, setStats] = useState<Stats | null>(null)
  // Bumped to start over: a fresh WebRTC attempt (then HLS if need be).
  const [attempt, setAttempt] = useState(0)
  // WebRTC sessions in a row that ended right after connecting: two mean WebRTC cannot carry this stream.
  const shortRuns = useRef(0)

  useEffect(() => {
    const v = video.current
    if (!v) return
    const run = { cancelled: false }
    const abort = new AbortController()
    const stopped = () => run.cancelled // a call: TypeScript would narrow a plain read across the awaits below
    let whep: WHEPSession | null = null
    let hls: { destroy: () => void } | null = null
    let statsTimer: ReturnType<typeof setInterval> | undefined
    let retryTimer: ReturnType<typeof setTimeout> | undefined
    const again = () => {
      if (stopped() || retryTimer) return
      retryTimer = setTimeout(() => {
        setAttempt((n) => n + 1)
      }, retryAfter)
    }
    setMode('connecting')
    setNote(null)
    setStats(null)

    const hlsURL = `/api/v1/${isPublic ? 'public' : 'live'}/hls/${path.split('/').map(encodeURIComponent).join('/')}/index.m3u8`
    const playHLS = async (reason: string) => {
      if (stopped()) return
      setNote(reason)
      setMode('hls')
      v.srcObject = null
      const { default: Hls } = await import('hls.js')
      if (stopped()) return
      if (Hls.isSupported()) {
        const h = new Hls({ lowLatencyMode: true, backBufferLength: 30 })
        hls = h
        h.on(Hls.Events.ERROR, (_e, data) => {
          if (data.fatal && !stopped()) {
            setMode('error')
            setNote(`HLS failed too (${data.details}); trying again…`)
            again()
          }
        })
        h.loadSource(hlsURL)
        h.attachMedia(v)
      } else {
        v.src = hlsURL // Safari plays HLS natively
      }
      void v.play().catch(() => undefined)
      let lastBytes = 0
      statsTimer = setInterval(() => {
        const q = v.getVideoPlaybackQuality()
        const bytes = q.totalVideoFrames // no byte counter for native playback; frames are enough for fps
        setStats({ width: v.videoWidth, height: v.videoHeight, fps: bytes - lastBytes })
        lastBytes = bytes
      }, 1000)
    }

    const connected = (pc: RTCPeerConnection) =>
      new Promise<void>((resolve, reject) => {
        const t = setTimeout(() => {
          reject(new Error(`no WebRTC connection within ${String(webrtcTimeout / 1000)} s`))
        }, webrtcTimeout)
        const check = () => {
          if (pc.connectionState === 'connected') {
            clearTimeout(t)
            resolve()
          } else if (pc.connectionState === 'failed') {
            clearTimeout(t)
            reject(new Error('the WebRTC connection failed'))
          }
        }
        pc.addEventListener('connectionstatechange', check)
        check()
      })

    void (async () => {
      try {
        whep = await startWHEP(path, v, { policy, signal: abort.signal, public: isPublic })
        await connected(whep.pc)
        if (stopped()) return
        setMode('webrtc')
        void v.play().catch(() => undefined)
        let last = { bytes: 0, t: 0 }
        let still = 0 // seconds without new video
        const session = whep
        const since = Date.now()
        // The session ended. Twice right after connecting means MediaMTX will not send this stream over WebRTC (it
        // closes readers of H.264 with B-frames), so HLS; otherwise the stream changed under it (the encoder took
        // over, the path restarted), so start over.
        const ended = () => {
          if (stopped() || whep !== session) return
          clearInterval(statsTimer)
          session.close()
          whep = null
          shortRuns.current = Date.now() - since < shortLived ? shortRuns.current + 1 : 0
          if (shortRuns.current >= 2) {
            shortRuns.current = 0
            void playHLS(
              'WebRTC keeps stopping right after it starts (the encoder may be sending B-frames, which WebRTC cannot play), so this plays over HLS, a few seconds behind.',
            )
          } else {
            setMode('connecting')
            again()
          }
        }
        session.pc.addEventListener('connectionstatechange', () => {
          const st = session.pc.connectionState
          if (st === 'disconnected' || st === 'failed' || st === 'closed') ended()
        })
        statsTimer = setInterval(() => {
          void session.pc.getStats().then((report) => {
            const videos: unknown[] = []
            report.forEach((s: RTCInboundRtpStreamStats & { kind?: string; type: string }) => {
              if (s.type !== 'inbound-rtp' || s.kind !== 'video') return
              videos.push(s)
              const bytes = s.bytesReceived ?? 0
              still = bytes > last.bytes ? 0 : still + 1
              const bps = last.t
                ? ((bytes - last.bytes) * 8 * 1000) / (s.timestamp - last.t)
                : undefined
              last = { bytes, t: s.timestamp }
              setStats({
                width: s.frameWidth ?? 0,
                height: s.frameHeight ?? 0,
                fps: s.framesPerSecond,
                bps,
              })
            })
            if (videos.length === 0) still++
            if (still >= stallAfter) ended()
          })
        }, 1000)
      } catch (err) {
        whep?.close()
        whep = null
        await playHLS(
          `WebRTC did not work (${err instanceof Error ? err.message : 'unknown error'}), so this plays over HLS, a few seconds behind.`,
        )
      }
    })()

    return () => {
      run.cancelled = true
      abort.abort()
      clearInterval(statsTimer)
      clearTimeout(retryTimer)
      whep?.close()
      hls?.destroy()
      v.srcObject = null
      v.removeAttribute('src')
    }
  }, [path, policy, isPublic, attempt])

  const label = { connecting: 'Connecting…', webrtc: 'WebRTC', hls: 'HLS', error: 'Not playing' }[
    mode
  ]
  return (
    <figure className={cn('relative overflow-hidden rounded-lg bg-black', className)}>
      <video
        ref={video}
        className="aspect-video w-full"
        muted
        autoPlay
        playsInline
        controls
        aria-label={`Live: ${path}`}
      />
      <figcaption className="pointer-events-none absolute top-2 left-2 flex flex-wrap gap-1.5 text-[11px]">
        <span
          className="rounded bg-black/70 px-1.5 py-0.5 text-white"
          data-testid="player-mode"
          data-mode={mode}
        >
          {label}
        </span>
        {showStats && stats && stats.width > 0 && (
          <span
            className="rounded bg-black/70 px-1.5 py-0.5 text-white tabular-nums"
            data-testid="player-stats"
          >
            {stats.width}×{stats.height}
            {stats.fps !== undefined ? ` · ${String(Math.round(stats.fps))} fps` : ''}
            {stats.bps !== undefined ? ` · ${formatBitrate(stats.bps)}` : ''}
          </span>
        )}
      </figcaption>
      {note && (
        <p
          className="border-t border-white/10 bg-black px-2 py-1 text-xs text-white/80"
          data-testid="player-note"
        >
          {note}
        </p>
      )}
    </figure>
  )
}
