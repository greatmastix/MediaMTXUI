// Where a stream is headed, and what that means for the encoder: presets with OBS settings, and health checks that
// compare what arrives with the preset, in plain words. The numbers follow the platforms' published recommendations
// (Twitch, YouTube) and what VRChat's video players handle well; they are guidance, not limits the server enforces.

export interface Target {
  id: string
  label: string
  /** One line on when to pick it. */
  use: string
  /** OBS settings, as OBS names them, in the order OBS shows them. */
  obs: { setting: string; value: string }[]
  /** Above this, the target drops frames or refuses the stream (kbit/s, video and audio together). */
  maxKbps: number
  maxWidth: number
  maxHeight: number
  /** Codecs the target plays, as MediaMTX names them. */
  video: string[]
  audio: string[]
  /** Something to know that the settings do not say. */
  note?: string
}

export const targets: Target[] = [
  {
    id: 'twitch',
    label: 'Twitch',
    use: 'Streaming on to Twitch (forwarding comes in a later version).',
    obs: [
      { setting: 'Output → Encoder', value: 'x264, NVENC H.264 or AMD H.264' },
      { setting: 'Rate control', value: 'CBR' },
      { setting: 'Bitrate', value: '6000 kbps (Twitch’s limit for most channels)' },
      { setting: 'Keyframe interval', value: '2 s' },
      { setting: 'Video → Output resolution', value: '1920×1080' },
      { setting: 'Common FPS values', value: '60 or 50' },
      { setting: 'Audio bitrate', value: '160 kbps, 48 kHz' },
    ],
    maxKbps: 6500,
    maxWidth: 1920,
    maxHeight: 1080,
    video: ['H264'],
    audio: ['MPEG-4 Audio'],
  },
  {
    id: 'youtube',
    label: 'YouTube',
    use: 'Streaming on to YouTube Live.',
    obs: [
      { setting: 'Output → Encoder', value: 'x264, NVENC H.264, or HEVC/AV1 if your GPU has it' },
      { setting: 'Rate control', value: 'CBR' },
      { setting: 'Bitrate', value: '9000–12000 kbps' },
      { setting: 'Keyframe interval', value: '2 s' },
      { setting: 'Video → Output resolution', value: '1920×1080' },
      { setting: 'Common FPS values', value: '60 or 50' },
      { setting: 'Audio bitrate', value: '128 kbps, 48 kHz' },
    ],
    maxKbps: 13000,
    maxWidth: 3840,
    maxHeight: 2160,
    video: ['H264', 'H265', 'AV1'],
    audio: ['MPEG-4 Audio'],
  },
  {
    id: 'vrchat-pc',
    label: 'VRChat (PC)',
    use: 'A video player in a VRChat world, for people on PC.',
    obs: [
      { setting: 'Output → Encoder', value: 'x264 or NVENC H.264' },
      { setting: 'Rate control', value: 'CBR' },
      { setting: 'Bitrate', value: '6000–8000 kbps' },
      { setting: 'Keyframe interval', value: '1 s (players join faster)' },
      { setting: 'Video → Output resolution', value: '1920×1080' },
      { setting: 'Common FPS values', value: '60 or 50' },
      { setting: 'Audio bitrate', value: '160 kbps AAC, 48 kHz' },
    ],
    maxKbps: 9000,
    maxWidth: 1920,
    maxHeight: 1080,
    video: ['H264'],
    audio: ['MPEG-4 Audio'],
    note: 'Paste the RTSP address from the Watch section into the world’s video player (AVPro).',
  },
  {
    id: 'vrchat-quest',
    label: 'VRChat (Quest)',
    use: 'A video player in a VRChat world, including people on Quest and phones.',
    obs: [
      { setting: 'Output → Encoder', value: 'x264 or NVENC H.264' },
      { setting: 'Rate control', value: 'CBR' },
      { setting: 'Bitrate', value: '5000–6000 kbps (Quest headsets fetch it over Wi-Fi)' },
      { setting: 'Keyframe interval', value: '1 s' },
      { setting: 'Video → Output resolution', value: '1920×1080' },
      { setting: 'Common FPS values', value: '60 or 50' },
      { setting: 'Audio bitrate', value: '128 kbps AAC, 48 kHz' },
    ],
    maxKbps: 6500,
    maxWidth: 1920,
    maxHeight: 1080,
    video: ['H264'],
    audio: ['MPEG-4 Audio'],
  },
  {
    id: 'low-latency',
    label: 'Low latency',
    use: 'Watching in the browser with under a second of delay (the watch link, WebRTC).',
    obs: [
      {
        setting: 'Output → Encoder',
        value: 'x264 (tune zerolatency) or NVENC H.264 (low latency)',
      },
      { setting: 'Rate control', value: 'CBR' },
      { setting: 'Bitrate', value: '5000–6000 kbps' },
      { setting: 'Keyframe interval', value: '1 s' },
      { setting: 'B-frames', value: '0' },
      { setting: 'Video → Output resolution', value: '1920×1080 at 60 or 50 fps' },
      { setting: 'Audio', value: 'publish with WHIP: OBS sends Opus, which browsers play' },
    ],
    maxKbps: 6500,
    maxWidth: 1920,
    maxHeight: 1080,
    video: ['H264', 'VP8', 'VP9', 'AV1'],
    audio: ['Opus'],
    note: 'Browsers play Opus audio over WebRTC but not AAC, which RTMP sends: over RTMP, WebRTC viewers get no sound.',
  },
]

export function targetById(id: string): Target | undefined {
  return targets.find((t) => t.id === id)
}

const videoCodecs = new Set([
  'AV1',
  'VP9',
  'VP8',
  'H265',
  'H264',
  'MPEG-4 Video',
  'MPEG-1/2 Video',
  'M-JPEG',
])

interface Track {
  codec?: string
  codecProps?: unknown
}

export interface Check {
  id: 'bitrate' | 'stability' | 'audio' | 'codec' | 'resolution'
  ok: boolean
  /** What is going on, in plain words. */
  text: string
  /** What to do about it (problems only). */
  fix?: string
}

function size(props: unknown): { width: number; height: number } | undefined {
  if (props && typeof props === 'object' && 'width' in props && 'height' in props) {
    const { width, height } = props
    if (typeof width === 'number' && typeof height === 'number' && width > 0)
      return { width, height }
  }
  return undefined
}

const kbps = (bps: number) => `${String(Math.round(bps / 1000))} kbps`

/**
 * Checks a live stream against its target: tracks as MediaMTX reports them, and recent bitrate samples (bit/s,
 * oldest first). Without a target, only the checks that need none run. Too few samples skip the bitrate checks.
 */
export function healthChecks(
  target: Target | undefined,
  tracks: Track[],
  samples: number[],
): Check[] {
  const out: Check[] = []
  const video = tracks.find((t) => t.codec !== undefined && videoCodecs.has(t.codec))
  const audio = tracks.find((t) => t.codec !== undefined && !videoCodecs.has(t.codec))

  if (samples.length >= 3) {
    const mean = samples.reduce((a, b) => a + b, 0) / samples.length
    if (target) {
      const over = mean > target.maxKbps * 1000
      out.push({
        id: 'bitrate',
        ok: !over,
        text: over
          ? `The bitrate (${kbps(mean)}) is above what ${target.label} takes well (${String(target.maxKbps)} kbps).`
          : `The bitrate (${kbps(mean)}) suits ${target.label}.`,
        fix: over ? 'Lower the bitrate in OBS (Settings → Output).' : undefined,
      })
    }
    const sd = Math.sqrt(samples.reduce((a, b) => a + (b - mean) ** 2, 0) / samples.length)
    const unstable = mean > 0 && sd / mean > 0.35
    out.push({
      id: 'stability',
      ok: !unstable,
      text: unstable ? 'The bitrate jumps around a lot.' : 'The bitrate is steady.',
      fix: unstable
        ? 'Use CBR in OBS. If it already is, your upload may be struggling: try a lower bitrate or SRT.'
        : undefined,
    })
  }

  out.push(
    audio
      ? { id: 'audio', ok: true, text: `Sound arrives (${audio.codec ?? ''}).` }
      : {
          id: 'audio',
          ok: false,
          text: 'No sound arrives with the stream.',
          fix: 'Check that OBS has an audio source and that it is not muted in the Audio Mixer.',
        },
  )

  if (target) {
    const bad = [video, audio].filter(
      (t): t is Track =>
        t?.codec !== undefined && ![...target.video, ...target.audio].includes(t.codec),
    )
    out.push(
      bad.length === 0
        ? { id: 'codec', ok: true, text: `${target.label} can play the codecs.` }
        : {
            id: 'codec',
            ok: false,
            text: `${target.label} cannot play ${bad.map((t) => t.codec).join(' or ')}.`,
            fix: `Use ${[target.video[0], target.audio[0]].filter(Boolean).join(' with ')} (${target.obs[0]?.value ?? ''}).`,
          },
    )
    const s = size(video?.codecProps)
    if (s) {
      const big = s.width > target.maxWidth || s.height > target.maxHeight
      const res = `${String(s.width)}×${String(s.height)}`
      out.push({
        id: 'resolution',
        ok: !big,
        text: big
          ? `The picture (${res}) is larger than ${target.label} needs (${String(target.maxWidth)}×${String(target.maxHeight)}).`
          : `The picture is ${res}.`,
        fix: big ? 'Lower the output resolution in OBS (Settings → Video).' : undefined,
      })
    }
  }
  return out
}
