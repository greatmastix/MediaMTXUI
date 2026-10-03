# Encoders: sending video

This page shows how to send video to a stream from OBS Studio, ffmpeg, an IP camera or a phone app, which settings
work well, and which protocol to pick. It is for streamers and for whoever sets up the cameras.

Everything here starts on the stream's page: under **Go live**, click **Show stream settings**. The page shows the
exact server address and key for each protocol your server has switched on, each with a copy button. Always copy them
from there; the examples below only show their shape. (New to streams? Read [Streams](streams.md) first.)

In the examples, the stream's path is `live/alice`, the stream host is `stream.example.com`, the key's name is
`key-abc` and its secret is `SECRET`. The UI's own web address is shown as `https://your-ui-address`.

## Which protocol to pick

Short version: use **RTMP** with OBS unless you have a reason not to. Use **SRT** on shaky connections (mobile data,
long distances), **WHIP** when browser viewers should be less than a second behind, and **RTSP** for cameras and
ffmpeg.

| Protocol | Delay to viewers | Port the encoder needs to reach | Works with | Audio it usually carries |
|---|---|---|---|---|
| RTMP | a second or two | 1935/tcp | OBS, Streamlabs, phone apps, most hardware encoders | AAC |
| SRT | a second or two, and it recovers lost packets | 8890/udp | OBS, ffmpeg, phone apps, many hardware encoders | AAC |
| WHIP | under a second to WebRTC viewers (the watch link, the preview) | the UI's web address (HTTPS) plus 8189/udp | OBS 30 and later | Opus |
| RTSP | a second or two | 8554/tcp (TCP only on this server) | ffmpeg, some IP cameras | AAC |

A port is a numbered door on the server; "tcp" and "udp" are two kinds of network traffic. If an encoder cannot
connect at all, the port may be closed by a firewall. See [Before you start](before-you-start.md) and
[Troubleshooting](troubleshooting.md).

Two things matter for browser viewers, whatever protocol you pick:

- **Sound in the browser.** Browsers play Opus audio over WebRTC, but not AAC, which RTMP, SRT and RTSP usually carry.
  So when you stream over RTMP, people watching over WebRTC get no sound; over HLS they do. Publish with WHIP (OBS
  sends Opus) when browser viewers should hear the stream with the lowest delay.
- **B-frames.** Browsers cannot play H.264 with B-frames over WebRTC. See [B-frames](#b-frames).

## OBS Studio

### RTMP (the usual way)

1. On the stream's page, click **Show stream settings** and find the **RTMP** row.
2. In OBS, open **Settings → Stream**.
3. Set **Service** to **Custom...**.
4. Paste the **Server** (for example `rtmp://stream.example.com/live`) into **Server**.
5. Paste the **Stream key** (for example `alice?user=key-abc&pass=SECRET`) into **Stream Key**.
6. Set **Output** and **Video** as described in [Recommended OBS settings](#recommended-obs-settings).
7. Click **OK**, then **Start Streaming**.

The stream's page turns **Live** within a few seconds.

> [!TIP]
> If your server runs [exposure control](exposure-control.md), keep the stream's page open the first time you stream
> from a new place. While the page is open, the server accepts encoders from your address, and after a successful
> stream it remembers that address for 30 days. This only helps when OBS runs on the same network as your browser.

### SRT

In **Settings → Stream**, choose **Service: Custom...**, paste the whole **SRT URL** from the page into **Server**
(it looks like `srt://stream.example.com:8890?streamid=publish:live/alice:key-abc:SECRET`) and leave **Stream Key**
empty.

The SRT URL carries everything in its `streamid`: `publish:` then the path, the key's name and its secret, separated
by colons.

### WHIP (OBS 30 and later)

WHIP sends WebRTC straight from OBS: the lowest delay to people watching in a browser, and Opus audio that browsers
can play.

1. In OBS, open **Settings → Stream** and set **Service** to **WHIP**.
2. Paste the WHIP **Server** from the page (for example `https://your-ui-address/whip/live/alice`).
3. Paste the **Bearer token** (for example `key-abc:SECRET`) into **Bearer Token**.
4. Click **OK**, then **Start Streaming**.

WHIP needs the UI's web address and UDP port 8189 on the server to be reachable.

### Recommended OBS settings

On the stream's page, **Encoder settings → Where is the stream headed?** picks a preset and shows a table of OBS
settings for it. A summary:

| Setting (as OBS names it) | Twitch | YouTube | VRChat (PC) | VRChat (Quest) | Low latency |
|---|---|---|---|---|---|
| Output → Encoder | x264, NVENC H.264 or AMD H.264 | x264, NVENC H.264, or HEVC/AV1 if your GPU has it | x264 or NVENC H.264 | x264 or NVENC H.264 | x264 (tune zerolatency) or NVENC H.264 (low latency) |
| Rate control | CBR | CBR | CBR | CBR | CBR |
| Bitrate | 6000 kbps | 9000–12000 kbps | 6000–8000 kbps | 5000–6000 kbps | 5000–6000 kbps |
| Keyframe interval | 2 s | 2 s | 1 s | 1 s | 1 s |
| Output resolution | 1920×1080 | 1920×1080 | 1920×1080 | 1920×1080 | 1920×1080 |
| FPS | 60 or 50 | 60 or 50 | 60 or 50 | 60 or 50 | 60 or 50 |
| Audio | 160 kbps, 48 kHz | 128 kbps, 48 kHz | 160 kbps AAC, 48 kHz | 128 kbps AAC, 48 kHz | publish with WHIP: Opus |
| B-frames | | | | | 0 |

Where to find these in OBS: switch **Settings → Output → Output Mode** to **Advanced** to see **Rate Control**,
**Bitrate**, **Keyframe Interval** and the encoder's options. Resolution and FPS are under **Settings → Video**; sample
rate and channels under **Settings → Audio**.

What the settings mean:

- **CBR** (constant bitrate) keeps the bitrate steady. The stream page's health check warns when it jumps around.
- **Keyframe interval**: how often a full picture is sent. Players can only start at one, so 1 s means viewers join
  faster; 2 s is what Twitch and YouTube ask for. OBS's 0 means "automatic", which is often longer.
- **Audio at 48 kHz, stereo**: OBS's usual setting, and what holding screens expect (see
  [Holding screens](holding-screens.md#your-encoder-must-match-the-clip)).

These numbers are guidance; the server does not enforce them.

### B-frames

B-frames are a compression trick that many encoders use by default (OBS's x264 among them). Browsers cannot play
H.264 with B-frames over WebRTC: MediaMTX closes such WebRTC sessions, so the preview and the watch link fall back to
HLS, a few seconds behind. The stream page then shows the note **B-frames from your encoder**.

To switch them off in OBS (Output Mode **Advanced**):

- **x264**: type `bframes=0` into **x264 Options**.
- **NVENC**: set **Max B-frames** to 0.

Other players (VLC, VRChat, HLS) play B-frames fine, so this only matters if people watch in the browser.

## ffmpeg

ffmpeg is a command-line tool that can send a file, a camera or a test picture. The examples below send a test
picture with a tone, so you can check a stream without a camera. Replace the address with the one from your stream's
page, and keep the quotes around it (the `?` and `&` in it would confuse the shell otherwise).

The settings in them: H.264 video with no B-frames (`-bf 0`) and a keyframe every second (`-g 30` at 30 fps), and
AAC audio at 48 kHz stereo, which also matches a holding screen.

RTMP:

```bash
ffmpeg -re -f lavfi -i testsrc2=size=1280x720:rate=30 -f lavfi -i sine=frequency=440:sample_rate=48000 -c:v libx264 -preset veryfast -tune zerolatency -bf 0 -g 30 -b:v 3000k -c:a aac -ar 48000 -ac 2 -b:a 128k -f flv "rtmp://stream.example.com/live/alice?user=key-abc&pass=SECRET"
```

For RTMP, ffmpeg takes one address: the page's **Server**, a slash, and the **Stream key**.

SRT:

```bash
ffmpeg -re -f lavfi -i testsrc2=size=1280x720:rate=30 -f lavfi -i sine=frequency=440:sample_rate=48000 -c:v libx264 -preset veryfast -tune zerolatency -bf 0 -g 30 -b:v 3000k -c:a aac -ar 48000 -ac 2 -b:a 128k -f mpegts "srt://stream.example.com:8890?streamid=publish:live/alice:key-abc:SECRET&pkt_size=1316"
```

RTSP (this server accepts RTSP over TCP only, hence `-rtsp_transport tcp`):

```bash
ffmpeg -re -f lavfi -i testsrc2=size=1280x720:rate=30 -f lavfi -i sine=frequency=440:sample_rate=48000 -c:v libx264 -preset veryfast -tune zerolatency -bf 0 -g 30 -b:v 3000k -c:a aac -ar 48000 -ac 2 -b:a 128k -f rtsp -rtsp_transport tcp "rtsp://key-abc:SECRET@stream.example.com:8554/live/alice"
```

To send a video file instead, replace the two `-f lavfi -i ...` inputs with `-i yourfile.mp4`. To send a file that is
already H.264 and AAC without re-encoding, use `-c copy` instead of the codec options (but then you keep whatever
B-frames and keyframe interval the file has).

When it works, ffmpeg keeps printing a status line with `frame=` and `speed=1x`, and the stream's page turns **Live**.

## IP cameras

There are two ways to get a camera's picture into MediaMTX.

### The camera sends (push)

Some cameras can send RTMP or RTSP to a server themselves. Create a stream for the camera and enter the stream's RTMP
or RTSP address from **Go live** in the camera's settings. If the camera asks for the RTMP server and key separately,
use the page's **Server** and **Stream key**; if it wants one address, join them with a slash.

### MediaMTX fetches it (pull)

Most cameras only offer an RTSP address that someone has to fetch, like `rtsp://user:password@192.168.1.20:554/stream1`.
Then MediaMTX connects to the camera. This is set up as a path in **Configuration**, which only admins can open:

- **Configuration → Quick setup → Re-stream a camera or stream**: enter a **Path name** and the address to **Pull
  from**. **Only connect while someone watches** saves bandwidth and the camera's resources; the first viewer waits a
  moment.
- Or **Configuration → Paths → New path**, and set **source** to **Pull from a URL (camera, server, stream)**.

MediaMTX can pull from `rtsp://`, `rtsps://`, `rtmp://`, `http(s)://` (HLS), `srt://`, `whep(s)://` and `moqt://`
addresses; credentials go in the address. Cameras on your local network are allowed. Addresses inside the server's
own Docker network, loopback and cloud metadata services are refused.

A pulled path is a plain path, not a stream: viewers and up can watch it on the **Watch** page, but it has no stream
page, keys or watch link. If you want those, an admin can afterwards create a stream with the same path name: an
existing path keeps its settings. See [Configuration](configuration.md) for more.

## Phone apps

Apps such as Larix Broadcaster can send RTMP or SRT from a phone. SRT is the better choice on mobile data, since it
recovers lost packets.

- **RTMP**: most apps want one address. Join the page's **Server** and **Stream key** with a slash, for example
  `rtmp://stream.example.com/live/alice?user=key-abc&pass=SECRET`. If the app has separate fields for the URL and the
  key, use the page's **Server** and **Stream key** as they are.
- **SRT**: paste the page's **SRT URL**. If the app has a separate field for the stream ID, put the host and port in
  the address (`srt://stream.example.com:8890`) and everything after `streamid=` into the stream ID field
  (`publish:live/alice:key-abc:SECRET`).

Set the app to H.264 video and AAC audio at 48 kHz, and to a keyframe interval of 1 or 2 seconds if it offers one.

## When it does not work

- **The stream page stays Offline**: check that you copied the whole key (it is long), that the port for the protocol
  is reachable, and look for a note at the top of the stream page.
- **"Your encoder was refused"**: the stream has a holding screen and your encoder sends different codecs. See
  [Holding screens](holding-screens.md#your-encoder-must-match-the-clip).
- **The browser plays only after a few seconds, over HLS**: B-frames (above) or UDP port 8189 blocked. See
  [Watching](watching.md#webrtc-and-the-hls-fallback).

More in [Troubleshooting](troubleshooting.md).
