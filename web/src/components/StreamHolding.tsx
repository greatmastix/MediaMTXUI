import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Clapperboard, Upload } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'

import { ApiError } from '@/api/client'
import { exportURL } from '@/api/recordings'
import {
  forwardsQuery,
  streamQuery,
  streamsQuery,
  updateStream,
  uploadHoldingClip,
  type Stream,
} from '@/api/streams'
import { fieldClass } from '@/components/config/SettingsForm'
import { Button } from '@/components/ui/button'
import { formatBytes, formatDuration } from '@/lib/format'
import { clearScratch, hasScratch, newScratch } from '@/lib/scratch'

// The holding screen (MediaMTX's alwaysAvailable): what viewers see while nobody streams here. Nothing, the offline
// screen (MediaMTX's "STREAM IS OFFLINE" card; the sidecar has it in every format and audio), or an own clip: a picture
// (made into a clip here) or a video (uploaded as it is, or transcoded here when it does not fit). Clips are in the
// stream's format, so the hand-over to the encoder changes neither size nor rate, and have the audio the encoder sends,
// since MediaMTX takes only an encoder that matches the clip. An own clip in another format or audio is chosen again.

type Format = Stream['format']
type Audio = Stream['audio']

const versionName: Record<Audio, string> = {
  aac: 'RTMP version (AAC)',
  opus: 'WHIP version (Opus)',
}

const formats: { value: Format; label: string }[] = [
  { value: '720p50', label: '720p, 50 fps' },
  { value: '720p60', label: '720p, 60 fps' },
  { value: '1080p50', label: '1080p, 50 fps' },
  { value: '1080p60', label: '1080p, 60 fps' },
]

function errorText(e: unknown, fallback: string) {
  return e instanceof ApiError || e instanceof Error ? e.message : fallback
}

const clock = (d: Date) =>
  d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' })
const day = (d: Date) =>
  d.toLocaleDateString([], { weekday: 'short', year: 'numeric', month: 'short', day: 'numeric' })

/**
 * A stretch of a path's recordings as one MP4, with the bytes so far reported as they arrive: into a scratch file
 * where the browser keeps files (an hour of recording is gigabytes), else into memory.
 */
async function fetchStretch(
  path: string,
  start: Date,
  seconds: number,
  onBytes: (n: number) => void,
): Promise<Blob> {
  const res = await fetch(exportURL(path, start, seconds, 'mp4', false), {
    credentials: 'same-origin',
  })
  if (!res.ok || !res.body) {
    let message = 'The recording cannot be read.'
    try {
      const body = (await res.json()) as { message?: unknown }
      if (typeof body.message === 'string') message = body.message
    } catch {
      // not the server's JSON error: keep the general sentence
    }
    throw new Error(message)
  }
  let n = 0
  const counted = res.body.pipeThrough(
    new TransformStream<Uint8Array, Uint8Array>({
      transform(chunk, ctl) {
        n += chunk.byteLength
        onBytes(n)
        ctl.enqueue(chunk)
      },
    }),
  )
  if (hasScratch()) {
    const file = await newScratch()
    await counted.pipeTo(await file.createWritable())
    return file.getFile()
  }
  return new Response(counted).blob()
}

/** A selected stretch of the stream's recordings, offered as its holding clip (from the recording page). */
export interface StretchFromRecording {
  start: Date
  seconds: number
}

export function Holding({
  stream: s,
  online,
  viewers,
  available,
  fromRecording,
  onFromRecordingDone,
}: {
  stream: Stream
  online: boolean
  viewers: number
  /** Something plays, so this page's own preview is one of the viewers. */
  available: boolean
  fromRecording?: StretchFromRecording
  /** The stretch was made the holding clip, or turned down. */
  onFromRecordingDone?: () => void
}) {
  const queryClient = useQueryClient()
  // A holding change restarts the stream in MediaMTX (it cannot change the clip on the fly): whoever streams, watches
  // or is forwarded to is cut off for a moment. Then the page asks first.
  const forwarding = useQuery(forwardsQuery(s.id)).data?.some((f) => f.enabled) ?? false
  const others = Math.max(0, viewers - (online || available ? 1 : 0)) // not counting this page's preview
  const cutOff = [
    online && 'your encoder is disconnected and reconnects',
    others > 0 &&
      `${String(others)} ${others === 1 ? 'person' : 'people'} watching drop for a moment`,
    forwarding && 'forwards to other platforms restart',
  ].filter((x): x is string => Boolean(x))
  const [pending, setPending] = useState<{ what: string; go: () => void } | null>(null)
  const guard = (what: string, go: () => void) => {
    if (cutOff.length > 0) setPending({ what, go })
    else go()
  }
  const fileInput = useRef<HTMLInputElement>(null)
  const [transcode, setTranscode] = useState(true)
  const [busy, setBusy] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [misfit, setMisfit] = useState<File | null>(null)
  const [notice, setNotice] = useState<string | null>(null)
  // A format or audio picked for an own clip that has to be chosen again first.
  const [wanted, setWanted] = useState<{ format: Format; audio: Audio } | null>(null)
  const format = wanted?.format ?? s.format
  const audio = wanted?.audio ?? s.audio
  const offline = s.holding === 'builtin'
  const own = s.holding === 'file'

  const saved = (updated: Stream) => {
    queryClient.setQueryData(streamQuery(s.id).queryKey, updated)
    void queryClient.invalidateQueries({ queryKey: streamsQuery.queryKey, exact: true })
  }
  const run = async (what: () => Promise<Stream>, onClipError?: () => void) => {
    setError(null)
    setMisfit(null)
    try {
      saved(await what())
      return true
    } catch (e) {
      setError(errorText(e, 'That did not work.'))
      if (e instanceof ApiError && e.kind === 'clip') onClipError?.()
      return false
    } finally {
      setBusy(null)
    }
  }
  const progress = (label: string) => (p: number) => {
    setBusy(`${label}… ${String(Math.round(p * 100))} %`)
  }
  const sent = (version: string) => (n: number, total: number) => {
    setBusy(
      `Uploading the ${version}… ${formatBytes(n)} of ${formatBytes(total)} (${String(Math.round((n / Math.max(total, 1)) * 100))} %)`,
    )
  }

  /**
   * An own clip, in the wanted format: the version for the wanted audio first, then the other one (the video copied,
   * only the sound encoded again), so switching between RTMP and WHIP needs no new upload. A picture is made into a
   * clip; a video is transcoded when asked to, or uploaded as it is. The source may be fetched first (a recording).
   */
  const take = (
    source: Blob | (() => Promise<Blob>),
    { image, transcode, onMisfit }: { image: boolean; transcode: boolean; onMisfit?: () => void },
  ) => {
    const other: Audio = audio === 'aac' ? 'opus' : 'aac'
    setNotice(null)
    return run(async () => {
      let clip: Blob = typeof source === 'function' ? await source() : source
      setBusy('Getting the encoder ready…')
      const lib = await import('@/lib/holdingClip')
      if (image || transcode) {
        const why = await lib.cannotEncode(audio, format)
        if (why) throw new Error(why)
        clip = image
          ? await lib.clipFromImage(
              clip,
              audio,
              format,
              progress(`Making the ${versionName[audio]}`),
            )
          : await lib.transcodeClip(
              clip,
              audio,
              format,
              progress(`Transcoding the ${versionName[audio]}`),
              s.holdingMaxBytes,
            )
      }
      setBusy(`Uploading the ${versionName[audio]}…`)
      saved(await uploadHoldingClip(s.id, clip, { audio, format }, sent(versionName[audio])))
      const why = await lib.cannotEncodeAudio(other)
      if (why) {
        setNotice(`Only the ${versionName[audio]} is stored. ${why}`)
        return queryClient.getQueryData(streamQuery(s.id).queryKey) ?? s
      }
      const second = await lib.withAudio(clip, other, progress(`Making the ${versionName[other]}`))
      setBusy(`Uploading the ${versionName[other]}…`)
      return uploadHoldingClip(s.id, second, { audio: other, format }, sent(versionName[other]))
    }, onMisfit).then((ok) => {
      void clearScratch()
      if (ok) setWanted(null)
      return ok
    })
  }

  const takeFile = (file: File, forceTranscode: boolean) => {
    const image = file.type.startsWith('image/')
    void take(file, {
      image,
      transcode: forceTranscode,
      onMisfit: image
        ? undefined
        : () => {
            setMisfit(file)
          },
    })
  }

  /** A stretch of this stream's recordings, fetched as an MP4 (as Download MP4 does) and transcoded like a video. */
  const takeRecording = (from: { start: Date; seconds: number }) => {
    void take(
      async () => {
        setBusy('Fetching the stretch from the recording…')
        return fetchStretch(s.name, from.start, from.seconds, (bytes) => {
          setBusy(`Fetching the stretch from the recording… ${formatBytes(bytes)}`)
        })
      },
      { image: false, transcode: true },
    ).then((ok) => {
      if (ok) {
        setNotice((n) => n ?? 'The stretch from the recording is your holding clip now.')
        onFromRecordingDone?.()
      }
    })
  }

  /**
   * A new format or audio: the offline screen follows at once; an own clip switches to its other version, and has to
   * be chosen again for another format (or a version it lacks).
   */
  const change = (next: { format: Format; audio: Audio }) => {
    setError(null)
    if (own) {
      if (next.format === s.format && s.clips[next.audio]) {
        setWanted(null)
        if (next.audio !== s.audio) {
          guard('Switching to the other version', () => {
            void run(() => updateStream(s.id, { audio: next.audio }))
          })
        }
      } else {
        setWanted(next)
      }
    } else if (s.holding === '') {
      void run(() => updateStream(s.id, next)) // nothing shows: nothing restarts
    } else {
      guard('Changing the holding screen', () => {
        void run(() => updateStream(s.id, next))
      })
    }
  }

  // Coming from the recording page, the offer is what this visit is for: it is scrolled into view.
  const fromRef = useRef<HTMLDivElement>(null)
  const fromAt = fromRecording?.start.getTime()
  useEffect(() => {
    const el = fromRef.current
    if (fromAt !== undefined && el && typeof el.scrollIntoView === 'function') {
      el.scrollIntoView({ block: 'center' })
    }
  }, [fromAt])

  const disabled = busy !== null
  return (
    <section className="space-y-3 rounded-xl border bg-card p-4" aria-labelledby="holding-title">
      <h2 id="holding-title" className="flex items-center gap-2 font-medium">
        <Clapperboard className="size-4 text-signal" aria-hidden /> Holding screen
      </h2>
      <p className="text-sm text-muted-foreground">
        What viewers see while nobody streams here: a picture, a video, or an offline screen.
        Players stay connected, and your encoder takes over when it connects. Forwarding to other
        platforms shows it too.
      </p>
      {fromRecording && (
        <div
          ref={fromRef}
          role="group"
          aria-label="Stretch from the recording"
          className="space-y-2 rounded-lg border border-signal/50 p-3 text-sm"
          data-testid="holding-from-recording"
        >
          <p>
            <span className="font-medium">From the recording:</span> {day(fromRecording.start)}{' '}
            {clock(fromRecording.start)} –{' '}
            {clock(new Date(fromRecording.start.getTime() + fromRecording.seconds * 1000))} (
            {formatDuration(fromRecording.seconds)}).
          </p>
          <p className="text-xs text-muted-foreground">
            It is transcoded here into {format} with {audio === 'aac' ? 'AAC' : 'Opus'}, as a video
            you choose below would be, and becomes your own clip, played on repeat while nobody
            streams. This needs Chrome or Edge and takes about as long as playing it.
          </p>
          <div className="flex flex-wrap gap-2">
            <Button
              size="sm"
              disabled={disabled}
              onClick={() => {
                guard('Putting up the clip from the recording', () => {
                  takeRecording(fromRecording)
                })
              }}
            >
              Make it the holding screen
            </Button>
            <Button
              size="sm"
              variant="outline"
              disabled={disabled}
              onClick={() => onFromRecordingDone?.()}
            >
              Not now
            </Button>
          </div>
        </div>
      )}

      <div className="grid gap-3 sm:grid-cols-2">
        <label className="space-y-1 text-sm">
          <span className="font-medium">Format</span>
          <select
            className={fieldClass}
            value={format}
            disabled={disabled}
            onChange={(e) => {
              change({ format: e.target.value as Format, audio })
            }}
          >
            {formats.map((f) => (
              <option key={f.value} value={f.value}>
                {f.label}
              </option>
            ))}
          </select>
          <span className="block text-xs text-muted-foreground">
            Match what your encoder sends, so the hand-over changes neither size nor rate.
          </span>
        </label>
        <label className="space-y-1 text-sm">
          <span className="font-medium">Your encoder sends audio as</span>
          <select
            className={fieldClass}
            value={audio}
            disabled={disabled}
            onChange={(e) => {
              change({ format, audio: e.target.value as Audio })
            }}
          >
            <option value="aac">AAC: RTMP or SRT (OBS’s usual)</option>
            <option value="opus">Opus: WHIP</option>
          </select>
          <span className="block text-xs text-muted-foreground">
            Follows your encoder by itself: MediaMTX takes only an encoder that sends what the
            holding clip has, so when one with the other audio connects, it is refused once, the
            holding screen switches, and the encoder&apos;s automatic reconnect gets in.
            {audio === 'aac' && ' In OBS: Settings → Audio → Sample Rate 48 kHz, Channels Stereo.'}
          </span>
        </label>
      </div>
      {wanted && (
        <p role="status" className="text-sm" data-testid="holding-wanted">
          Your clip is in {s.format} with {s.audio === 'aac' ? 'AAC' : 'Opus'}. Choose your file
          again below to make it {wanted.format} with {wanted.audio === 'aac' ? 'AAC' : 'Opus'}.
        </p>
      )}

      <fieldset className="space-y-1.5 text-sm" disabled={disabled}>
        <legend className="font-medium">Show while offline</legend>
        <label className="flex items-center gap-2">
          <input
            type="radio"
            name="holding"
            checked={s.holding === ''}
            onChange={() => {
              guard('Switching the holding screen off', () => {
                void run(() => updateStream(s.id, { holding: '' }))
              })
            }}
          />
          Nothing (players wait or give up)
        </label>
        <label className="flex items-center gap-2">
          <input
            type="radio"
            name="holding"
            checked={offline}
            onChange={() => {
              guard('Switching to the offline screen', () => {
                void run(() => updateStream(s.id, { holding: 'builtin' }))
              })
            }}
          />
          Offline screen
        </label>
        <label className="flex items-center gap-2">
          <input
            type="radio"
            name="holding"
            checked={own}
            disabled={!s.clips[s.audio]}
            onChange={() => {
              guard('Switching to your clip', () => {
                void run(() => updateStream(s.id, { holding: 'file' }))
              })
            }}
          />
          My own clip
          {!s.clips[s.audio] && (
            <span className="text-xs text-muted-foreground">(choose a file below)</span>
          )}
        </label>
      </fieldset>

      <div className="space-y-2 text-sm">
        <p className="font-medium">Your own picture or video</p>
        <div className="flex flex-wrap items-center gap-3">
          <input
            ref={fileInput}
            type="file"
            accept="image/*,video/*"
            className="sr-only"
            aria-label="Picture or video for the holding screen"
            onChange={(e) => {
              const f = e.target.files?.[0]
              e.target.value = ''
              if (f) {
                guard('Putting up a new clip', () => {
                  takeFile(f, transcode)
                })
              }
            }}
          />
          <Button
            variant="outline"
            size="sm"
            disabled={disabled}
            onClick={() => fileInput.current?.click()}
          >
            <Upload /> Choose a file
          </Button>
          <label className="flex items-center gap-2">
            <input
              type="checkbox"
              checked={transcode}
              disabled={disabled}
              onChange={(e) => {
                setTranscode(e.target.checked)
              }}
            />
            Transcode videos here (fits any video; takes about as long as playing it). Untick to
            upload one that already fits as it is.
          </label>
        </div>
        <p className="text-xs text-muted-foreground">
          A picture becomes a short clip in {format}. A video goes up as it is if it is already
          H.264 in {format} with {audio === 'aac' ? 'AAC-LC 48 kHz stereo' : 'Opus stereo'} audio
          (at most {s.holdingMaxBytes ? formatBytes(s.holdingMaxBytes) : 'the server’s limit'} and
          an hour); otherwise it is transcoded here, an hour at most. A long clip takes a while to
          make and to upload, and MediaMTX needs a few seconds more to start it, about 5 s an hour.
        </p>
      </div>

      {(s.clips.aac || s.clips.opus) && (
        <p className="text-xs text-muted-foreground" data-testid="holding-versions">
          Your clip is stored as: {s.clips.aac ? '✓' : '✗'} RTMP version (AAC) ·{' '}
          {s.clips.opus ? '✓' : '✗'} WHIP version (Opus), in {s.format}.
        </p>
      )}
      {notice && (
        <p role="status" className="text-sm">
          {notice}
        </p>
      )}
      {pending && (
        <div
          role="alertdialog"
          aria-label="This interrupts the stream"
          className="space-y-2 rounded-lg border border-warning/50 p-3 text-sm"
          data-testid="holding-confirm"
        >
          <p>
            {pending.what} restarts the stream in MediaMTX: {cutOff.join('; ')}. Browsers reconnect
            by themselves; other players (VRChat, VLC) may need a moment or a restart.
          </p>
          <div className="flex gap-2">
            <Button
              size="sm"
              onClick={() => {
                const { go } = pending
                setPending(null)
                go()
              }}
            >
              Change it anyway
            </Button>
            <Button
              size="sm"
              variant="outline"
              onClick={() => {
                setPending(null)
              }}
            >
              Cancel
            </Button>
          </div>
        </div>
      )}
      {busy && (
        <p role="status" className="text-sm" data-testid="holding-busy">
          {busy}
        </p>
      )}
      {error && (
        <div role="alert" className="space-y-2 text-sm text-destructive">
          <p>{error}</p>
          {misfit && (
            <Button
              size="sm"
              variant="outline"
              onClick={() => {
                guard('Putting up a new clip', () => {
                  takeFile(misfit, true)
                })
              }}
            >
              Transcode it here and upload
            </Button>
          )}
        </div>
      )}
    </section>
  )
}
