# Streams

This page explains what a stream is in MediaMTX UI and walks through a stream's page from top to bottom: its keys,
the addresses for your encoder, the watch link, public and private, recording, guest keys and more. It is for
everyone who runs or uses streams: admins who create them, and streamers who go live on them.

![A stream's page while it is live](screenshots/stream.png)

## What a stream is

MediaMTX, the streaming server underneath, works with **paths**: a path is a name such as `live/alice` that an
encoder sends video to and players read video from. A **stream** in MediaMTX UI is a path with a few things added:

- an **owner** (optional): the account that may run it,
- a **stream key** (to send video) and a separate **playback key** (to watch with outside players),
- a **page** with the addresses for your encoder, a preview, viewer numbers and settings,
- a **watch link** that anyone can open in a browser, while the stream is public.

The stream's path name is part of every address, so pick something short: `live/alice`, `church`, `studio/cam1`.

## Who can do what

| | Admin | Operator | Owner of the stream | Viewer | Streamer (not the owner) |
|---|---|---|---|---|---|
| Create a stream, choose its owner, delete it | yes | no | no | no | no |
| Show and renew keys, change settings, forwarding, guest keys, holding screen | yes | yes | yes | no | no |
| Open the stream's page, preview, charts | yes | yes | yes | yes (read only) | no, does not see it |

A **streamer** account sees only the streams it owns, and only the simple part of the UI (**Streams**, **Watch** and
**Account**). See [People and roles](people.md) for how to invite people.

## Create a stream

Only admins create streams.

1. Open **Streams** in the menu and click **New stream**.
2. Fill in:
   - **Path**: the stream's name in MediaMTX, for example `live/alice`. It is part of every address. Use letters,
     digits and `_ . ~ - /`. A path that already has a stream is refused.
   - **Title**: what people see on the page and on the watch link, for example `Alice live`. Leave it empty and the
     path is used. Up to 80 characters.
   - **Owner**: the person who streams here. Choose **Nobody (admins and operators manage it)** if no streamer
     account should run it. Invite a streamer on the **People** page first if they do not have an account yet.
3. Click **Create stream**.

You land on the new stream's page. The server has made a stream key and a playback key, and added the path to
MediaMTX's configuration. If a path of that name was already in the configuration, it keeps its settings.

> [!NOTE]
> New streams are **public**: anyone with the watch link can watch them, without an account. Nobody can stream to a
> stream without its stream key. To change that, see [Public or private](#public-or-private).

## The stream's page, section by section

At the top you see the title, a **Live** or **Offline** badge, and below it the path, the owner ("streamed by ..."),
and while live: how many people are watching, the bitrate coming in and the codecs (for example `H264 + Opus`).
Viewers, operators and admins also get links to **Path details** (MediaMTX's view of the path) and **Recordings**.

The sections below appear for people who manage the stream (its owner, operators and admins), unless noted.

### Notes about your encoder

Sometimes MediaMTX writes something about your encoder into its log that nothing else would show you. The page reads
the log and shows a note at the top for 15 minutes:

- **Your encoder was refused**: MediaMTX turned your encoder away because what it sends does not match the stream's
  holding clip (for example, it sends a different audio codec). The note says what it sends and what is expected. See
  [Holding screens](holding-screens.md#your-encoder-must-match-the-clip).
- **Holding screen switched**: your encoder sends the other kind of audio, so the holding screen switched to its
  other version. The first attempt was refused; OBS reconnects by itself within a few seconds.
- **B-frames from your encoder**: your encoder sends B-frames, which browsers cannot play over WebRTC. The preview
  and the watch link then fall back to HLS, a few seconds behind. Fix: in OBS, set B-frames to 0 (for x264, type
  `bframes=0` into **x264 Options**; for NVENC, set **Max B-frames** to 0). See [Encoders](encoders.md#b-frames).

### How the stream is doing

While you are live, this box checks what arrives and says **All good.** or how many things to look at:

- **Bitrate**: whether it suits the destination you picked (see [Encoder settings](#encoder-settings-presets-and-how-tos)).
- **Steadiness**: if the bitrate jumps around a lot, use CBR (constant bitrate) in OBS, or try a lower bitrate or SRT.
- **Sound**: whether any audio arrives at all.
- **Codecs** and **resolution**: whether the destination can play them and whether the picture is larger than it
  needs.

Without a destination picked, only the steadiness and sound checks run.

### Go live

This is where your encoder's settings are. Click **Show stream settings** to see the server address and stream key
for each protocol that MediaMTX has switched on. Each value has a copy button. The secret parts stay masked until you
click **Show key**.

The key is like a password: anyone who has it can stream here as you. Each time someone shows it, the
[audit log](logs-and-audit.md) records it.

The addresses look like this (for a stream `live/alice` on `stream.example.com`, with the default ports):

| Protocol | When to use it | What the page shows |
|---|---|---|
| RTMP | OBS, Streamlabs and most encoders: the usual choice. | **Server** `rtmp://stream.example.com/live` and **Stream key** `alice?user=key-abc&pass=SECRET` |
| SRT | Unstable connections (mobile, long distance): recovers lost packets. | **URL** `srt://stream.example.com:8890?streamid=publish:live/alice:key-abc:SECRET` |
| WHIP | OBS 30+ (service WHIP): lowest delay to WebRTC viewers. | **Server** `https://your-ui-address/whip/live/alice` and **Bearer token** `key-abc:SECRET` |
| RTSP | Cameras and ffmpeg. | **URL** `rtsp://key-abc:SECRET@stream.example.com:8554/live/alice` |

Notes:

- The host in RTMP, SRT and RTSP addresses is the stream host your admin set during install (`MTXUI_PUBLIC_HOST`).
  WHIP goes through the UI's own web address.
- RTMP is split the way OBS wants it: the last part of the path goes into the stream key, together with the
  credentials. A path without a slash (for example `church`) gives the server `rtmp://stream.example.com` and the key
  `church?user=...`.
- A protocol that is switched off in MediaMTX is not listed. If none is, the page says so; an admin can switch
  RTMP or SRT on in **Configuration**.

[Encoders](encoders.md) shows how to put these into OBS, ffmpeg, a camera or a phone app.

Below the addresses the page tells you about the stream's key (its name, when it was made and when it was last used),
and, when the server runs [exposure control](exposure-control.md), whether it currently accepts encoders from your
address:

- "While this page is open, the server accepts encoders from your address (...); after a successful stream it
  remembers that address for 30 days." So keep the page open the first time you stream from a new place.
- Otherwise the page says why not, and that an admin can open the ports on the **Exposure** page.

Without exposure control (the default), the stream ports are simply open and this line does not appear.

### Make a new stream key

Click **New key** (shown after the settings are revealed), then **Make a new key**. The new key works at once. The old
one stops at once: an encoder streaming with it is disconnected and needs the new key. Do this when a key may have
leaked, or to lock out an encoder for good.

If a stream has no key at all, the page says "This stream has no key yet" with a **Make one** link.

### Encoder settings (presets and how-tos)

**Where is the stream headed?** picks a preset. The page then shows the OBS settings that suit it, and the health
checks compare your stream with it while you are live. Picking a preset saves it at once. The presets:

| Preset | For | Bitrate | Keyframe interval | Notes |
|---|---|---|---|---|
| Twitch | Streaming on to Twitch | 6000 kbps | 2 s | x264, NVENC H.264 or AMD H.264; CBR; 1920×1080, 60 or 50 fps; audio 160 kbps, 48 kHz |
| YouTube | Streaming on to YouTube Live | 9000–12000 kbps | 2 s | x264, NVENC H.264, or HEVC/AV1; CBR; 1920×1080, 60 or 50 fps; audio 128 kbps, 48 kHz |
| VRChat (PC) | A video player in a VRChat world, for people on PC | 6000–8000 kbps | 1 s | x264 or NVENC H.264; CBR; audio 160 kbps AAC |
| VRChat (Quest) | Including people on Quest and phones | 5000–6000 kbps | 1 s | x264 or NVENC H.264; CBR; audio 128 kbps AAC |
| Low latency | Watching in the browser with under a second of delay | 5000–6000 kbps | 1 s | x264 (tune zerolatency) or NVENC (low latency); **B-frames 0**; publish with WHIP for Opus audio |

These are guidance, not limits: the server does not enforce them. [Encoders](encoders.md#recommended-obs-settings)
explains the settings.

Three short how-tos follow: **How to: stream with OBS**, **How to: show the stream in VRChat** and **How to: keep
the delay low**.

### Forwarding

Sends the stream on to Twitch, YouTube, Kick or another server while you stream here. See
[Forwarding](forwarding.md).

### Guest keys

Lets someone stream here, or watch a private stream, for a limited time without an account. See
[Guest keys](#guest-keys-1) below.

### Holding screen

What viewers see while nobody streams: nothing, an offline screen, or your own picture or video. See
[Holding screens](holding-screens.md).

### Watch (public streams only)

Anyone may see this section when the stream is public. It has:

- the **Watch link**, for example `https://your-ui-address/s/live/alice`: "anyone can open it in a browser, no
  account." This is the link to share. See [Watching](watching.md#the-watch-link).
- **Keyless addresses** for outside players and servers: RTSP, RTMP, SRT and WHEP, without any key. See
  [Watching](watching.md#outside-players).

### Outputs with the playback key

Addresses for players and other servers (VRChat, VLC, OBS) that carry the **playback key**. The playback key lets
people watch, never stream, so sharing these addresses never lets anyone take over your stream. On a public stream the
section is called **Outputs with the playback key** and is there for when you make the stream private; on a private
stream it is called **Outputs**.

Click **Show outputs**, then **Show key** to unmask. **New playback key** and **Make a new playback key** replace the
key: the old addresses stop at once and players using them are disconnected.

The page offers RTSP, RTMP, SRT and WHEP addresses. It does not offer a plain HLS address, because the key would end
up in the web server's logs. People with an account can watch in the browser on the **Watch** page instead.

### Preview

A player for the stream, with resolution, frame rate and bitrate on top. It appears by itself when you start
streaming (or when a holding screen plays). It counts as one viewer while it plays. See
[Watching](watching.md#the-player) for how it plays.

### Charts

**Bitrate in** and **Viewers**, for the last hour (live), 24 hours, 7 days or 30 days. The longer ranges come from the
server's stored history, which keeps 30 days by default (`MTXUI_HISTORY_DAYS`, see [Settings reference](config.md)).

### Settings

- **Title**: the name on the page and the watch link.
- **Viewer limit**: the most people who may watch at once. `0 means no limit.` MediaMTX refuses viewers beyond it.
  The largest value is 100000.
- **Public**: "Anyone can watch with the watch link or a keyless address. Streaming to it always needs the stream
  key." See below.
- **Record this stream**: MediaMTX records what is streamed here (not the holding screen), within the server's
  storage budget; the oldest recordings go first. If the disk runs critically low, the server switches recording off
  for every path until an admin switches it back on. See [Recordings](recordings.md).
- **Owner** (admins only): who runs the stream, or **Nobody**.

Click **Save**. Only the fields you changed are sent, so a change someone else made meanwhile is not undone.

**Disconnect the encoder** (shown while live) closes the encoder's connection. "It can reconnect with the same key;
make a new key to keep it out." It works for encoders that connect to the server; a source MediaMTX pulls itself
(a camera set up in Configuration) cannot be disconnected here.

**Delete stream** (admins only) asks again with **Delete live/alice and its keys**. See [Delete a stream](#delete-a-stream).

## Public or private

New streams are public. A public stream:

- can be watched by anyone with its watch link, in a browser, without an account,
- can be read by outside players at its keyless addresses (RTSP, RTMP, SRT, WHEP),
- still needs the stream key to stream to it. Public means watching only. It never allows publishing, and never
  playback of recordings.

To make a stream private, untick **Public** under **Settings** and click **Save**. The owner, operators and admins can
do this. Anonymous viewers are disconnected within about 2 seconds, the watch link stops working ("There is no public
stream at this address."), and from then on people need one of these to watch:

- a signed-in account that may see the stream (viewers and up see every stream; a streamer sees its own),
- the playback key (the **Outputs** addresses),
- a guest watch key (below).

## Owners and streamer accounts

Give a stream to a person by making them its **Owner**: admins choose the owner when creating the stream or later in
**Settings**. The owner can then do everything on the stream's page except deleting it and changing the owner.

A typical setup for someone who only streams: invite them with the **streamer** role on the [People](people.md)
page, then create a stream with them as the owner. When they sign in they see **Streams** with their streams, the
**Watch** page and their **Account**, nothing else.

A stream with no owner is run by operators and admins.

## Guest keys

Guest keys let someone stream here for a while (a co-host, a stand-in), or watch while the stream is private, without
an account. A guest key works for this stream only and stops by itself.

### Make one

1. On the stream's page, under **Guest keys**, click **New guest key**.
2. Fill in:
   - **For**: who it is for, for example `Bob (co-host)` (up to 60 characters).
   - **Lets them**: **Stream here** or **Watch**.
   - **Valid for**: **1 hour**, **6 hours**, **1 day**, **3 days** or **1 week**.
3. Click **Make the key**.

The page then shows the addresses the guest needs, with the key already in them: for **Stream here** the same
RTMP, SRT, WHIP and RTSP settings as under **Go live**; for **Watch** the RTSP, RTMP, SRT and WHEP addresses as under
**Outputs**. **They are shown only now.** Copy them and send them privately: anyone with them can stream or watch.
Click **Done** when you have them.

A guest watch key works with outside players. It does not open the watch link, which only works for public streams.

### What happens next

- The valid keys are listed with who they are for, what they allow, **Valid until** a time, who made them and when
  they were last used.
- When a key expires, whatever it opened is disconnected. A guest stream does not run on past its key.
- **Revoke**, then **Revoke now**, ends a key at once and disconnects the guest.
- The last five expired and revoked keys stay listed under **Expired and revoked**.
- A stream can have at most 10 valid guest keys at once.

### Guest keys that stream

- A guest who starts streaming takes over from whoever streams here at the time.
- If the server runs [exposure control](exposure-control.md): the server does not know the guest's address, so while
  a guest stream key is valid, the stream ports stay open to **everyone**. Only someone with a key can stream, but the
  ports are reachable. Revoke the key when the guest is done if you want them closed again.

## Delete a stream

Admins click **Delete stream** under **Settings**, then **Delete live/alice and its keys**. This:

- revokes the stream key, the playback key and every guest key, and disconnects whatever they opened,
- removes the path from MediaMTX's configuration (with its forwards and holding screen),
- deletes the stream's holding clips.

It does not delete recordings already made. A new stream with the same path later gets new keys; the old ones never
work again.

## Related pages

- [Encoders](encoders.md): OBS, ffmpeg, cameras and phone apps.
- [Watching](watching.md): the player, the Watch page, the watch link and outside players.
- [Forwarding](forwarding.md) and [Holding screens](holding-screens.md).
- [Recordings](recordings.md) and [People and roles](people.md).
