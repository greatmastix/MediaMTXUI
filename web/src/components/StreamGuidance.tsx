import { useMutation, useQueryClient } from '@tanstack/react-query'
import { CircleAlert, CircleCheck, Compass } from 'lucide-react'
import { useState } from 'react'

import { ApiError } from '@/api/client'
import { streamQuery, streamsQuery, updateStream, type Stream } from '@/api/streams'
import { fieldClass } from '@/components/config/SettingsForm'
import { healthChecks, targetById, targets } from '@/lib/targets'

// Guidance for the people who stream: where the stream is headed (a preset with the OBS settings for it), short
// how-tos, and while live, health checks against that preset in plain words.

/** The target picker, its OBS settings and the how-tos. Picking a target saves it at once. */
export function Guidance({ stream: s }: { stream: Stream }) {
  const queryClient = useQueryClient()
  const target = targetById(s.target)
  const save = useMutation({
    mutationFn: (id: string) => updateStream(s.id, { target: id }),
    onSuccess: (updated) => {
      queryClient.setQueryData(streamQuery(s.id).queryKey, updated)
      void queryClient.invalidateQueries({ queryKey: streamsQuery.queryKey, exact: true })
    },
  })
  return (
    <section className="space-y-3 rounded-xl border bg-card p-4" aria-labelledby="guidance-title">
      <h2 id="guidance-title" className="flex items-center gap-2 font-medium">
        <Compass className="size-4 text-signal" aria-hidden /> Encoder settings
      </h2>
      <label className="block max-w-sm space-y-1 text-sm">
        <span className="font-medium">Where is the stream headed?</span>
        <select
          className={fieldClass}
          value={target?.id ?? ''}
          disabled={save.isPending}
          onChange={(e) => {
            save.mutate(e.target.value)
          }}
        >
          <option value="">Not chosen</option>
          {targets.map((t) => (
            <option key={t.id} value={t.id}>
              {t.label}
            </option>
          ))}
        </select>
        <span className="block text-xs text-muted-foreground">
          {target
            ? target.use
            : 'Pick one for the OBS settings that suit it and checks while you are live.'}
        </span>
      </label>
      {target && (
        <>
          <table
            className="w-full max-w-2xl text-sm"
            aria-label={`OBS settings for ${target.label}`}
          >
            <tbody>
              {target.obs.map((o) => (
                <tr key={o.setting} className="border-t first:border-t-0">
                  <th
                    scope="row"
                    className="w-56 py-1.5 pr-3 text-left font-normal text-muted-foreground"
                  >
                    {o.setting}
                  </th>
                  <td className="py-1.5">{o.value}</td>
                </tr>
              ))}
            </tbody>
          </table>
          {target.note && <p className="text-xs text-muted-foreground">{target.note}</p>}
        </>
      )}
      {save.error && (
        <p className="text-sm text-destructive" role="alert">
          {save.error instanceof ApiError ? save.error.message : 'The target was not saved.'}
        </p>
      )}
      <HowTos />
    </section>
  )
}

function HowTo({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <details className="rounded-lg border px-3 py-2 text-sm">
      <summary className="cursor-pointer font-medium">{title}</summary>
      <div className="mt-2 space-y-2 text-muted-foreground">{children}</div>
    </details>
  )
}

function HowTos() {
  return (
    <div className="space-y-2">
      <HowTo title="How to: stream with OBS">
        <ol className="list-decimal space-y-1 pl-5">
          <li>Click Show stream settings above and copy the RTMP Server and Stream key.</li>
          <li>
            In OBS, open Settings → Stream, pick Service <em>Custom…</em>, and paste the Server and
            the Stream key.
          </li>
          <li>
            Set Output and Video as in the table above (pick where the stream is headed first).
          </li>
          <li>Click Start Streaming. This page turns Live within a few seconds.</li>
        </ol>
        <p>
          Keep this page open the first time: while it is open, the server accepts your encoder from
          your address, and remembers it for 30 days after a stream.
        </p>
      </HowTo>
      <HowTo title="How to: show the stream in VRChat">
        <ol className="list-decimal space-y-1 pl-5">
          <li>
            Pick VRChat as the target above and stream with those settings: H.264 video and AAC
            sound, a 1 s keyframe interval.
          </li>
          <li>
            Copy the RTSP address from the Watch section (or, for a private stream, the RTSP output
            with the playback key).
          </li>
          <li>
            Paste it into the world&apos;s video player (one that uses AVPro, for live streams).
          </li>
        </ol>
      </HowTo>
      <HowTo title="How to: keep the delay low">
        <ul className="list-disc space-y-1 pl-5">
          <li>
            Publish with WHIP (OBS 30 or later: Service <em>WHIP</em>) and watch with the watch
            link: under a second behind.
          </li>
          <li>Set a 1 s keyframe interval and no B-frames; use CBR.</li>
          <li>
            RTMP and RTSP add a second or two; HLS adds several. Players buffer on top, so a player
            setting for low latency helps too.
          </li>
        </ul>
      </HowTo>
    </div>
  )
}

interface Track {
  codec?: string
  codecProps?: unknown
}

/** While live: the checks against the target, from the tracks and the last minute of bitrate. */
export function Health({
  stream: s,
  tracks,
  inBps,
  ratesAt,
}: {
  stream: Stream
  tracks: Track[]
  inBps: number | undefined
  ratesAt: number
}) {
  // The last dozen rates (about a minute), added as they arrive (React's "adjust state while rendering").
  const [samples, setSamples] = useState<number[]>([])
  const [seen, setSeen] = useState(ratesAt)
  if (ratesAt !== seen) {
    setSeen(ratesAt)
    if (inBps !== undefined) setSamples([...samples.slice(-11), inBps])
  }
  const checks = healthChecks(targetById(s.target), tracks, samples)
  const problems = checks.filter((c) => !c.ok).length
  return (
    <section className="space-y-2 rounded-xl border bg-card p-4" aria-labelledby="health-title">
      <h2 id="health-title" className="font-medium">
        How the stream is doing
      </h2>
      <p className="text-sm text-muted-foreground" data-testid="health-summary">
        {problems === 0
          ? 'All good.'
          : `${String(problems)} thing${problems === 1 ? '' : 's'} to look at.`}
        {!targetById(s.target) && ' Pick where the stream is headed for more checks.'}
      </p>
      <ul className="space-y-1.5">
        {checks.map((c) => (
          <li
            key={c.id}
            className="flex items-start gap-2 text-sm"
            data-testid={`health-${c.id}`}
            data-ok={c.ok}
          >
            {c.ok ? (
              <CircleCheck className="mt-0.5 size-4 shrink-0 text-good" aria-label="Fine" />
            ) : (
              <CircleAlert className="mt-0.5 size-4 shrink-0 text-warning" aria-label="Problem" />
            )}
            <span>
              {c.text}
              {c.fix && <span className="block text-muted-foreground">{c.fix}</span>}
            </span>
          </li>
        ))}
      </ul>
    </section>
  )
}
