# Watching

This page covers every way to watch a stream: the player on a stream's page, the **Watch** page with several players
in a grid, the public watch link, outside players such as VLC, OBS or VRChat, and the viewer numbers and charts. It
also says what embedding on other websites can and cannot do. It is for anyone who watches, shares or monitors
streams.

![The Watch page with four players](screenshots/watch.png)

## Who can watch what

| Who | Where | What |
|---|---|---|
| Anyone, no account | the [watch link](#the-watch-link) and the [keyless addresses](#outside-players) | public streams only |
| Anyone with the playback key or a guest watch key | [outside players](#outside-players) | that one stream, public or private |
| Streamers (signed in) | the **Watch** page and their stream pages | the streams they own |
| Viewers, operators and admins (signed in) | the **Watch** page, stream pages, the **Dashboard** | every stream and path |

Watching never allows streaming. New streams are public; see [Streams](streams.md#public-or-private) for how to make
one private.

## The player

The stream's page has a **Preview**, the **Watch** page has one player per tile, and the watch link has one large
player. They all work the same way:

- A label in the corner says how it plays: **Connecting...**, **WebRTC**, **HLS** or **Not playing**.
- The player starts **muted**, because browsers only start video by themselves without sound. Use the speaker
  button in the player's controls to hear it.
- If the stream stops and starts again (the encoder reconnects, the encoder takes over from the holding screen, or a
  setting change restarts the stream), the player starts again by itself.
- While nobody streams, it shows "Start streaming and the preview appears here by itself" (stream page) or "Not live
  right now. The video starts here by itself when it goes live." (watch link). With a
  [holding screen](holding-screens.md), the holding clip plays instead.

### WebRTC and the HLS fallback

The player tries two ways to play, in this order:

1. **WebRTC**: under a second behind. The video itself travels over **UDP port 8189** on the server.
2. **HLS**: a few seconds behind, but it works over the normal web connection.

It switches to HLS and says why under the picture when:

- WebRTC does not connect within 6 seconds, for example because UDP port 8189 is blocked by a firewall on the server
  or on the viewer's network: "WebRTC did not work (...), so this plays over HLS, a few seconds behind."
- WebRTC connects but keeps stopping right after it starts. That happens when the encoder sends **B-frames**, which
  WebRTC cannot play: "WebRTC keeps stopping right after it starts (the encoder may be sending B-frames, which WebRTC
  cannot play), so this plays over HLS, a few seconds behind." The fix is in the encoder: see
  [Encoders](encoders.md#b-frames).

Sound over WebRTC needs Opus audio, which OBS sends with WHIP. An encoder that sends AAC (RTMP, SRT, most cameras)
plays without sound over WebRTC; over HLS the sound plays. See [Encoders](encoders.md#which-protocol-to-pick).

## The Watch page

**Watch** in the menu shows one to nine players at once, for example to keep an eye on several cameras.

- **Grid**: **1 player**, **2 × 2** or **3 × 3**.
- Above each tile, a drop-down picks what it shows. It lists every stream and path you may watch; ones that are not
  live are marked **(offline)**, and ones showing their holding screen **(holding screen)**. A tile set to a stream
  that is not live says "... is not live. It starts playing here when it goes live." and does so by itself.
- **Stats** shows the resolution, frame rate and bitrate on each player.

The page remembers the grid you had on screen and brings it back next time, in any browser where you sign in.

To keep several arrangements, type a **Name** (for example `Front of house`) and click **Save layout**. Open one later
from **Saved layout**. To remove one, open it (its name appears in **Name**) and click **Delete**. Saved layouts belong
to your account.

You can also open the Watch page on one stream: while a path is live, its **Path details** page (viewers and up) has
a player and a link **Open in the multi-view**.

## The watch link

A public stream's page has a **Watch** section with its **Watch link**, for example
`https://your-ui-address/s/live/alice`. "Anyone can open it in a browser, no account." This is the address to share
with your audience: on social media, in a chat, as a QR code.

The watch link page shows the stream's title, a badge (**Live**, **Back soon** while a holding screen plays, or
**Offline**) and the player, with the same WebRTC-then-HLS behaviour as above.

- It only ever shows public streams. If the stream is private, deleted or never existed, it says "There is no public
  stream at this address."
- Making the stream private disconnects people watching through it within about 2 seconds.

## Outside players

Players and other servers can read a stream directly from MediaMTX's stream ports. The stream's page lists the
addresses with copy buttons:

- In the **Watch** section (public streams): addresses **without any key**.
- In **Outputs** (called **Outputs with the playback key** on a public stream): the same addresses **with the
  playback key**. Use these for a private stream, or to keep working after you make the stream private. The playback
  key lets people watch, never stream.
- A guest watch key shows the same addresses once, with the guest key in them (see
  [Guest keys](streams.md#guest-keys-1)).

| Protocol | What the page suggests it for | Address with the playback key (keyless version: without the credentials) |
|---|---|---|
| RTSP | VRChat on PC, VLC, and most players and cameras | `rtsp://view-abc:SECRET@stream.example.com:8554/live/alice` |
| RTMP | An OBS media source, or another server re-streaming it | `rtmp://stream.example.com/live/alice?user=view-abc&pass=SECRET` |
| SRT | Other encoders and servers over long or shaky links | `srt://stream.example.com:8890?streamid=read:live/alice:view-abc:SECRET` |
| WHEP | OBS (WHEP source) and other WHEP players: WebRTC, under a second behind | **Server** `https://your-ui-address/whep/live/alice`, **Bearer token** `view-abc:SECRET` |

The keyless versions are `rtsp://stream.example.com:8554/live/alice`, `rtmp://stream.example.com/live/alice`,
`srt://stream.example.com:8890?streamid=read:live/alice` and the WHEP **Server** without a token.

Only protocols switched on in MediaMTX are listed.

### VLC

Open **Media → Open Network Stream** and paste the RTSP address. This server serves RTSP over TCP only; if VLC
shows nothing, start it with RTSP over TCP from a terminal:

```bash
vlc --rtsp-tcp "rtsp://view-abc:SECRET@stream.example.com:8554/live/alice"
```

### ffplay

ffplay comes with ffmpeg and plays any of the addresses, for example SRT:

```bash
ffplay "srt://stream.example.com:8890?streamid=read:live/alice:view-abc:SECRET"
```

### VRChat

Pick **VRChat (PC)** or **VRChat (Quest)** under **Encoder settings** on the stream's page and stream with those
settings (H.264 video, AAC sound, a 1 s keyframe interval). Copy the RTSP address from the **Watch** section (or, for
a private stream, the RTSP output with the playback key) and paste it into the world's video player (one that uses
AVPro, for live streams).

The page does not offer a plain HLS address, which VRChat on Quest would need, because the key would end up in the web
server's logs.

### OBS

Add a **Media Source** with the RTMP or RTSP address (untick **Local File**), or use a WHEP source with the WHEP
address and bearer token.

## Embedding on another website

What works: **link to the watch link**, or open it in a new window or tab. It is a complete page with a player that
needs no account.

What does not work: building your own web player on another website that talks to this server's WHEP endpoint. The
WHIP and WHEP endpoints send no CORS headers (the rules that let a page on one site call another site), so a web page
on another site cannot use them. This is deliberate; see [Security](security.md). Desktop and app players (OBS, VLC,
VRChat) are not web pages and are not affected.

## Viewer numbers and charts

- **Stream page**: next to the title, "N watching" and the bitrate coming in, while live. The **Viewers** and
  **Bitrate in** charts show the last hour live, or 24 hours, 7 days or 30 days from the stored history. Your own
  preview counts as one viewer while it plays.
- **Streams** list: each card shows "N watching" and the bitrate while live.
- **Dashboard** (viewers, operators and admins): **Audience** (readers and client sessions now), **Traffic** (out
  and in), a **Streams** strip with "Live · N watching" per stream, and **History** charts for **Bandwidth** (in and
  out) and **Audience** (readers and clients) over 1 h, 24 h, 7 d or 30 d.

A "reader" is anything that reads a stream: a browser, VLC, VRChat, another server. The server keeps a summary of
every minute for 30 days by default (`MTXUI_HISTORY_DAYS`, see [Settings reference](config.md)).
