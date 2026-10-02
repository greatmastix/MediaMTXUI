import {
  ALL_FORMATS,
  AudioBufferSource,
  BlobSource,
  BufferTarget,
  CanvasSink,
  CanvasSource,
  canEncodeAudio,
  canEncodeVideo,
  Conversion,
  Input,
  Mp4OutputFormat,
  Output,
} from 'mediabunny'

// Holding clips made in the browser (loaded only when someone makes one): a picture becomes a short clip, a video is
// transcoded, all to what the stream's encoder sends: H.264 in the stream's format plus stereo
// 48 kHz audio, AAC for RTMP/SRT or Opus for WHIP (MediaMTX lets an encoder in only if it matches the clip; the same
// size and rate make the hand-over seamless). Encoding uses the browser's WebCodecs encoders, so no video decoder ever
// runs on the server; the server checks the result and puts the tracks in order.

export type ClipAudio = 'aac' | 'opus'
export type ClipFormat = '720p50' | '720p60' | '1080p50' | '1080p60'

const formats: Record<ClipFormat, { width: number; height: number; fps: number; bitrate: number }> =
  {
    '720p50': { width: 1280, height: 720, fps: 50, bitrate: 2_500_000 },
    '720p60': { width: 1280, height: 720, fps: 60, bitrate: 3_000_000 },
    '1080p50': { width: 1920, height: 1080, fps: 50, bitrate: 4_500_000 },
    '1080p60': { width: 1920, height: 1080, fps: 60, bitrate: 5_000_000 },
  }

const maxSeconds = 120

/** Why this browser cannot make a clip with that audio and format, or null when it can. */
export async function cannotEncode(audio: ClipAudio, format: ClipFormat): Promise<string | null> {
  if (typeof VideoEncoder === 'undefined' || typeof AudioEncoder === 'undefined') {
    return 'This browser cannot encode video (it has no WebCodecs). Use Chrome or Edge.'
  }
  const { width, height } = formats[format]
  if (!(await canEncodeVideo('avc', { width, height }))) {
    return 'This browser cannot encode H.264 video.'
  }
  if (!(await canEncodeAudio(audio, { numberOfChannels: 2, sampleRate: 48000 }))) {
    return audio === 'aac'
      ? 'This browser cannot encode AAC audio (Chrome and Edge can on Windows and macOS, not on Linux). Choose Opus, or make the clip elsewhere.'
      : 'This browser cannot encode Opus audio.'
  }
  return null
}

function newOutput() {
  const target = new BufferTarget()
  const output = new Output({ format: new Mp4OutputFormat({ fastStart: 'in-memory' }), target })
  return { output, target }
}

function blobOf(target: BufferTarget) {
  if (!target.buffer) throw new Error('The clip came out empty.')
  return new Blob([target.buffer], { type: 'video/mp4' })
}

function silence(seconds: number) {
  return new AudioBuffer({
    length: Math.max(1, Math.round(48000 * seconds)),
    numberOfChannels: 2,
    sampleRate: 48000,
  })
}

/** Draws an image into the frame, whole, centred on black. */
function drawContained(
  ctx: OffscreenCanvasRenderingContext2D,
  img: CanvasImageSource & { width: number; height: number },
) {
  const { width, height } = ctx.canvas
  ctx.fillStyle = '#000'
  ctx.fillRect(0, 0, width, height)
  const scale = Math.min(width / img.width, height / img.height)
  const w = img.width * scale
  const h = img.height * scale
  ctx.drawImage(img, (width - w) / 2, (height - h) / 2, w, h)
}

function frame(format: ClipFormat) {
  const { width, height } = formats[format]
  const canvas = new OffscreenCanvas(width, height)
  const ctx = canvas.getContext('2d')
  if (!ctx) throw new Error('No 2D canvas.')
  return { canvas, ctx }
}

/** A picture on repeat with silence: `seconds` long (MediaMTX loops it). */
export async function clipFromImage(
  file: Blob,
  audio: ClipAudio,
  format: ClipFormat,
  onProgress?: (p: number) => void,
  seconds = 4,
): Promise<Blob> {
  const { fps } = formats[format]
  const { canvas, ctx } = frame(format)
  const bitmap = await createImageBitmap(file)
  drawContained(ctx, bitmap)
  bitmap.close()
  const { output, target } = newOutput()
  const video = new CanvasSource(canvas, {
    codec: 'avc',
    bitrate: formats[format].bitrate / 2,
    keyFrameInterval: 1,
  })
  const sound = new AudioBufferSource({ codec: audio, bitrate: 128_000 })
  output.addVideoTrack(video, { frameRate: fps })
  output.addAudioTrack(sound)
  await output.start()
  const frames = seconds * fps
  for (let i = 0; i < frames; i++) {
    await video.add(i / fps, 1 / fps)
    onProgress?.(i / frames)
  }
  await sound.add(silence(seconds))
  await output.finalize()
  return blobOf(target)
}

/** A video transcoded to fit (at most two minutes), with silence when it has no sound. */
export async function transcodeClip(
  file: Blob,
  audio: ClipAudio,
  format: ClipFormat,
  onProgress?: (p: number) => void,
): Promise<Blob> {
  const { width, height, fps, bitrate } = formats[format]
  const input = new Input({ source: new BlobSource(file), formats: ALL_FORMATS })
  const videoTrack = await input.getPrimaryVideoTrack()
  if (!videoTrack) throw new Error('The file has no video this browser can read.')
  const duration = Math.min(await input.computeDuration(), maxSeconds)
  const { output, target } = newOutput()

  if (await input.getPrimaryAudioTrack()) {
    const conversion = await Conversion.init({
      input,
      output,
      tracks: 'primary',
      trim: { end: duration },
      video: {
        codec: 'avc',
        width,
        height,
        fit: 'contain',
        frameRate: fps,
        bitrate,
        keyFrameInterval: 1,
        forceTranscode: true,
      },
      audio: {
        codec: audio,
        numberOfChannels: 2,
        sampleRate: 48000,
        bitrate: 128_000,
        forceTranscode: true,
      },
    })
    if (!conversion.isValid) {
      throw new Error(
        `The video cannot be converted here: ${conversion.discardedTracks.map((d) => d.reason).join(', ')}.`,
      )
    }
    if (onProgress) conversion.onProgress = onProgress
    await conversion.execute()
    return blobOf(target)
  }

  // No sound: frame by frame into the format's frame at its rate, then silence of the same length.
  const { canvas, ctx } = frame(format)
  const video = new CanvasSource(canvas, { codec: 'avc', bitrate, keyFrameInterval: 1 })
  const sound = new AudioBufferSource({ codec: audio, bitrate: 128_000 })
  output.addVideoTrack(video, { frameRate: fps })
  output.addAudioTrack(sound)
  await output.start()
  // Each output frame shows the input frame current at its time, so the clip has the format's rate exactly.
  const start = await videoTrack.getFirstTimestamp()
  const frames = Math.floor(duration * fps)
  const times = Array.from({ length: frames }, (_, i) => start + i / fps)
  let i = 0
  for await (const shot of new CanvasSink(videoTrack).canvasesAtTimestamps(times)) {
    if (shot) drawContained(ctx, shot.canvas)
    await video.add(i / fps, 1 / fps)
    onProgress?.(i / frames)
    i++
  }
  await sound.add(silence(duration))
  await output.finalize()
  return blobOf(target)
}

/** Why this browser cannot make a clip's version with that audio (the video is copied), or null when it can. */
export async function cannotEncodeAudio(audio: ClipAudio): Promise<string | null> {
  if (typeof AudioEncoder === 'undefined')
    return 'This browser cannot encode audio (it has no WebCodecs).'
  if (!(await canEncodeAudio(audio, { numberOfChannels: 2, sampleRate: 48000 }))) {
    return audio === 'aac'
      ? 'This browser cannot encode AAC audio (Chrome and Edge can on Windows and macOS, not on Linux).'
      : 'This browser cannot encode Opus audio.'
  }
  return null
}

/** The same clip with the other audio: the video is copied as it is, only the sound is encoded again. */
export async function withAudio(
  clip: Blob,
  audio: ClipAudio,
  onProgress?: (p: number) => void,
): Promise<Blob> {
  const input = new Input({ source: new BlobSource(clip), formats: ALL_FORMATS })
  const { output, target } = newOutput()
  const conversion = await Conversion.init({
    input,
    output,
    tracks: 'primary',
    audio: {
      codec: audio,
      numberOfChannels: 2,
      sampleRate: 48000,
      bitrate: 128_000,
      forceTranscode: true,
    },
  })
  if (!conversion.isValid) {
    throw new Error(
      `The other version cannot be made here: ${conversion.discardedTracks.map((d) => d.reason).join(', ')}.`,
    )
  }
  if (onProgress) conversion.onProgress = onProgress
  await conversion.execute()
  return blobOf(target)
}
