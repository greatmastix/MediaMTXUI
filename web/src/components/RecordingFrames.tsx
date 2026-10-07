import { useEffect, useState } from 'react'

import { exportURL } from '@/api/recordings'

// The first and the last frame of a selected stretch, so you see where an export begins and ends before you play or
// download it. Each comes from a one-second export (MP4, which states its duration): MediaMTX starts an export at the
// keyframe before the asked time and stacks the frames up to it at the start, so the clip's first picture is the one
// at the asked time, and its last picture the one just before its end. The browser decodes them; the server cannot.
// The second waits for the first: exports share a few slots (MTXUI_EXPORT_CONCURRENCY), and Play needs one.

const clipSeconds = 1
const settleMs = 400 // a dragged selection changes many times: ask once it holds still

const clock = (d: Date) =>
  d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' })

export function RecordingFrames({ name, first, last }: { name: string; first: Date; last: Date }) {
  const [settled, setSettled] = useState<{ first: number; last: number } | null>(null)
  const f = first.getTime()
  const l = last.getTime()
  useEffect(() => {
    const t = setTimeout(() => {
      setSettled({ first: f, last: l })
    }, settleMs)
    return () => {
      clearTimeout(t)
    }
  }, [f, l])
  // A new stretch starts both frames afresh; until it holds still, they wait.
  const active = settled?.first === f && settled.last === l
  return (
    <Frames
      key={active ? `${String(f)}-${String(l)}` : 'waiting'}
      name={name}
      first={f}
      last={l}
      active={active}
    />
  )
}

function Frames({
  name,
  first,
  last,
  active,
}: {
  name: string
  first: number
  last: number
  active: boolean
}) {
  const [firstDone, setFirstDone] = useState(false)
  const seconds = Math.min(clipSeconds, Math.max(0.1, (last - first) / 1000))
  return (
    <div className="grid max-w-2xl grid-cols-2 gap-3" data-testid="recording-frames">
      <Frame
        label="First frame"
        at={first}
        src={active ? exportURL(name, new Date(first), seconds, 'mp4', false) : null}
        end={false}
        onDone={() => {
          setFirstDone(true)
        }}
      />
      <Frame
        label="Last frame"
        at={last}
        src={
          active && firstDone
            ? exportURL(name, new Date(last - seconds * 1000), seconds, 'mp4', false)
            : null
        }
        end
      />
    </div>
  )
}

function Frame({
  label,
  at,
  src,
  end,
  onDone,
}: {
  label: string
  at: number
  src: string | null
  end: boolean
  onDone?: () => void
}) {
  const [state, setState] = useState<'loading' | 'ready' | 'failed'>('loading')
  return (
    <figure className="space-y-1">
      <div
        className="relative aspect-video w-full overflow-hidden rounded-md bg-black"
        data-testid={end ? 'last-frame' : 'first-frame'}
        data-state={state}
      >
        {src && (
          <video
            src={src}
            muted
            playsInline
            preload="auto"
            aria-label={`${label} of the selection`}
            className="h-full w-full object-contain"
            onLoadedMetadata={(e) => {
              const v = e.currentTarget
              // Past the stacked frames at the start; or onto the last frame (just before the end).
              v.currentTime =
                end && Number.isFinite(v.duration) ? Math.max(0, v.duration - 0.01) : 0.04
            }}
            onSeeked={() => {
              setState('ready')
              onDone?.()
            }}
            onError={() => {
              setState('failed')
              onDone?.()
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
