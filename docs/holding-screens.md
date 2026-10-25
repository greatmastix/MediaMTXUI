# Holding screens

A holding screen is what a stream shows while nobody streams to it: an "offline" card, or your own picture or video
on repeat. This page explains what it does, how to put up your own clip, and the one rule that catches people out:
your encoder has to match the clip. It is for streamers and whoever manages a stream.

## What it does

Without a holding screen, a stream simply stops when your encoder disconnects. Players show an error or give up, and
the watch link says "Offline".

With a holding screen, the stream keeps running in MediaMTX (MediaMTX calls this `alwaysAvailable`):

- the holding clip plays on repeat while nobody streams,
- players stay connected: browsers, VLC, VRChat and other servers keep playing,
- when your encoder connects, it takes over, and when it disconnects, the clip comes back,
- the watch link shows **Back soon** instead of **Offline**, and the Watch page marks the stream **(holding screen)**,
- [forwards](forwarding.md) keep running, so the platforms you forward to show the holding clip too,
- [recording](recordings.md) skips the holding clip: only what your encoder sends is recorded.

## Set one up

On the stream's page, find **Holding screen**.

1. **Format**: choose what your encoder sends: **720p, 50 fps**, **720p, 60 fps**, **1080p, 50 fps** or **1080p, 60
   fps**. Holding clips are made in this format, so the hand-over between clip and encoder changes neither size nor
   frame rate (some players, VRChat's among them, stutter on such a change).
2. **Your encoder sends audio as**: **AAC: RTMP or SRT (OBS's usual)** or **Opus: WHIP**. This follows your encoder
   by itself (see [below](#the-audio-follows-your-encoder)), so the starting choice is not critical.
3. Under **Show while offline**, choose:
   - **Nothing (players wait or give up)**: no holding screen.
   - **Offline screen**: a ready-made "stream is offline" card. It exists in every format and with both kinds of
     audio, so it always matches the choices above.
   - **My own clip**: your picture or video (choose a file first, see below).

## Your own picture or video

Under **Your own picture or video**, click **Choose a file** and pick a picture or a video.

- **A picture** becomes a short clip (four seconds of the picture with silence, on repeat) in the stream's format.
  The picture is shown whole, centred on black.
- **A video** is, with **Transcode videos here** ticked (the default), converted in your browser to fit: H.264 in
  the stream's format with the right audio. This takes about as long as playing the video, and only the first hour
  is used. A video without sound gets silence.
- **Untick Transcode videos here** to upload a video that already fits as it is: an MP4 (not a fragmented one) with
  H.264 video in the stream's format and no B-frames, AAC-LC audio at 48 kHz stereo (or Opus stereo for WHIP),
  at most 3.9 GB and at most an hour long. If it does not fit, the page says what is wrong and offers **Transcode it
  here and upload**.

The page shows its progress ("Transcoding the RTMP version (AAC)... 40 %", then "Uploading ..."). When it is done,
the holding screen switches to **My own clip**.

The conversion happens in your browser, not on the server, using the browser's built-in video encoders (WebCodecs).
Chrome and Edge can do it. If a browser cannot, the page says so, for example "This browser cannot encode video (it
has no WebCodecs). Use Chrome or Edge."

### A stretch of a recording

A stream that is recorded can use part of its recording as the holding screen, for a replay or a loop of the last
show:

1. Open **Recordings**, then the stream, and select the stretch on the timeline (at most an hour). Its first and last
   frame show under it.
2. Press **Use as holding screen**. The stream's page opens with the stretch under **Holding screen**, "From the
   recording: ...".
3. Press **Make it the holding screen**. The page fetches the stretch, transcodes it into the stream's format and
   audio, and uploads both versions, showing its progress, as with a video you choose. **Not now** leaves it.

The button shows for streams you manage. It needs a browser that can encode video (Chrome or Edge).

### Two versions of your clip

Your clip is stored twice: an **RTMP version (AAC)** for encoders that send AAC (RTMP, SRT) and a **WHIP version
(Opus)** for WHIP. The page makes the second one right after the first; only the sound is encoded again. That way the
holding screen can switch between them without a new upload. The page shows what is stored, for example "Your clip is
stored as: ✓ RTMP version (AAC) · ✓ WHIP version (Opus), in 1080p50."

> [!WARNING]
> Chrome and Edge cannot encode AAC audio on Linux. There, making a clip with **AAC** selected fails ("Choose Opus,
> or make the clip elsewhere"), and with **Opus: WHIP** selected you get only the WHIP version: the page says "Only the
> WHIP version (Opus) is stored." An encoder that sends AAC (RTMP, SRT, OBS's usual) is then refused while
> your clip is up. Make the clip on Windows or macOS, or use the **Offline screen**.

### Changing the format later

The offline screen follows a new format at once. Your own clip does not: after you pick another **Format**, the page
asks you to choose your file again so it can be made in the new format.

## Your encoder must match the clip

This is the important part. While a holding screen is up, MediaMTX only accepts an encoder that sends the **same
kinds of tracks as the clip**:

- **H.264 video** (not H.265/HEVC, not AV1),
- and audio of the same kind: **AAC-LC at 48 kHz, stereo** (what OBS sends over RTMP and SRT by default) or **Opus
  stereo** (what OBS sends over WHIP).

An encoder that sends something else is refused. The stream's page then shows **Your encoder was refused**, with what
your encoder sends and what the holding screen expects, for example "MediaMTX refused your encoder: it sends H265 +
AAC, but the holding screen expects H264 + AAC. Set what your encoder sends under Holding screen, or switch the
holding screen off."

What to check in OBS:

- **Settings → Audio**: **Sample Rate** 48 kHz, **Channels** Stereo.
- **Settings → Output**: an H.264 encoder (x264, NVENC H.264, AMD H.264).
- An audio track at all: an encoder without sound is refused too, since the clip has sound (silence counts).

The **Format** (size and frame rate) is not checked, but matching it gives a smooth hand-over.

### The audio follows your encoder

If an encoder with the other kind of audio connects (say you switch from RTMP to WHIP), MediaMTX refuses it once.
The server notices, switches the holding screen to the clip's other version, and the encoder's automatic reconnect
gets in. OBS retries every few seconds. The page shows **Holding screen switched**: "... MediaMTX had to refuse the
first attempt; OBS reconnects by itself within a few seconds (otherwise, start streaming again)."

This needs both versions of your clip. If the version is missing, the note says to choose the clip again, so both
versions are made, or to switch the holding screen off.

## Changes interrupt the stream briefly

MediaMTX cannot swap a holding clip on the fly; any change to the holding screen restarts the stream. If that would
interrupt someone, the page asks first and lists who: your encoder is disconnected and reconnects, people watching
drop for a moment, forwards to other platforms restart. "Browsers reconnect by themselves; other players (VRChat,
VLC) may need a moment or a restart." Click **Change it anyway** or **Cancel**.

## Limits

- An uploaded clip is at most 3.9 GB and at most an hour long. Change the size with `MTXUI_HOLDING_MAX_GB` (0.01 to
  3.9) in a `compose.override.yaml`; MediaMTX cannot read an MP4 of 4 GB or more. The upload may take as long as it
  keeps arriving (it is cut off when nothing arrives for 2 minutes), and the page shows how far it is.
- Storing a clip needs twice its size free on the server for a moment, without going below the free space at which
  recording is switched off (`MTXUI_RECORDINGS_CRITICAL_FREE_GB`, 5 GB): otherwise the page says how much is free.
- A video transcoded in the browser is cut to its first hour. A long one takes about as long to make as to play, and
  then to upload. Chrome and Edge keep it on your computer's disk meanwhile, not in memory.
- **Long clips slow MediaMTX down a little.** It reads a clip whole whenever it loads its configuration and whenever
  the stream starts, about 5 seconds for each hour of clip (at 50 fps). Saving a setting takes that much longer while
  an hour-long clip is up, and the stream takes that long to come back after a holding change.
- Backups carry holding clips up to 64 MB; larger ones are left out (see [Backups](backups.md#what-a-backup-contains)).
- One upload per stream at a time.
- Clips must not have B-frames, since browsers could not play them over WebRTC. Transcoding in the browser takes care
  of that.

## Exposure control

If your server runs [exposure control](exposure-control.md) with its default rules, the stream ports open to
everyone while any stream has a holding screen (a holding screen counts as "something is live"). Keys still decide
who may stream or watch. An admin who wants the ports open only to known addresses can switch the viewers rule off on
the **Exposure** page.

## Related pages

- [Streams](streams.md): the stream's page.
- [Encoders](encoders.md): OBS settings, including audio.
- [Forwarding](forwarding.md): forwards run while the holding screen plays.
