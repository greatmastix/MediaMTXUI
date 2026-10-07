import { useEffect, useState } from 'react'

import { frameURL } from '@/api/recordings'

// The first and the last frame of a selected stretch, so you see where an export begins and ends before you play or
// download it. The server cannot decode video: each frame is a tenth of a second of MP4 (GET /v1/recordings/frame)
// that the browser decodes and stops on. Most of its size is the frames since the keyframe before it, so an encoder's
// keyframe interval decides how fast frames load. Each frame loads on its own once its moment holds still (a handle
// being dragged reloads only its own frame), both at once; frames have server slots of their own, apart from Play's.

const settleMs = 250
const longerSeconds = 2

const clock = (d: Date) =>
  d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' })

export function RecordingFrames({ name, first, last }: { name: string; first: Date; last: Date }) {
  return (
    <div className="grid max-w-2xl grid-cols-2 gap-3" data-testid="recording-frames">
      <SettledFrame name={name} at={first.getTime()} edge="first" />
      <SettledFrame name={name} at={last.getTime()} edge="last" />
    </div>
  )
}

function SettledFrame({ name, at, edge }: { name: string; at: number; edge: 'first' | 'last' }) {
  const [settled, setSettled] = useState<number | null>(null)
  useEffect(() => {
    const t = setTimeout(() => {
      setSettled(at)
    }, settleMs)
    return () => {
      clearTimeout(t)
    }
  }, [at])
  const active = settled === at
  return (
    <Frame
      key={active ? `${name} ${String(at)}` : 'waiting'}
      label={edge === 'first' ? 'First frame' : 'Last frame'}
      at={at}
      src={active ? (seconds) => frameURL(name, new Date(at), edge, seconds) : null}
      end={edge === 'last'}
    />
  )
}

function Frame({
  label,
  at,
  src,
  end,
}: {
  label: string
  at: number
  src: ((seconds?: number) => string) | null
  end: boolean
}) {
  const [state, setState] = useState<'loading' | 'ready' | 'failed'>('loading')
  const [why, setWhy] = useState('') // the browser's media error, for whoever looks into a frame that stays black
  // A tenth of a second first; where that holds no picture (a recording's start, where audio comes first, or its end),
  // two seconds, once.
  const [longer, setLonger] = useState(false)
  return (
    <figure className="space-y-1">
      <div
        className="relative aspect-video w-full overflow-hidden rounded-md bg-black"
        data-testid={end ? 'last-frame' : 'first-frame'}
        data-state={state}
        data-error={why || undefined}
      >
        {src && (
          <video
            src={src(longer ? longerSeconds : undefined)}
            muted
            playsInline
            preload="auto"
            aria-label={`${label} of the selection`}
            className="h-full w-full object-contain"
            onLoadedMetadata={(e) => {
              const v = e.currentTarget
              if (v.videoWidth === 0) {
                if (longer) {
                  setState('failed')
                } else {
                  setLonger(true)
                }
                return
              }
              // Past the stacked frames at the start; or onto the last frame (just before the end).
              v.currentTime =
                end && Number.isFinite(v.duration) ? Math.max(0, v.duration - 0.01) : 0.04
            }}
            onSeeked={() => {
              setState('ready')
            }}
            onError={(e) => {
              const err = e.currentTarget.error
              setWhy(err ? `${String(err.code)} ${err.message}` : '')
              setState('failed')
            }}
          />
        )}
        {(!src || state !== 'ready') && (
          <p className="absolute inset-0 grid place-items-center text-xs text-white/70">
            {state === 'failed' ? 'No picture' : 'Loading…'}
          </p>
        )}
      </div>
      <figcaption className="text-xs text-muted-foreground">
        {label} · {clock(new Date(at))}
      </figcaption>
    </figure>
  )
}
