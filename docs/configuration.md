# Configuring MediaMTX

MediaMTX, the streaming server inside MediaMTX UI, is configured by one file, `mediamtx.yml`. You change it on the
**Configuration** page, never by hand. This page explains each tab, how every change is checked before MediaMTX sees
it, which settings are locked and why, and how protocols relate to the ports on your server. It is for admins.

**Short version:** for everyday streaming you do not need this page at all: create streams on **Streams**. Use
**Configuration** to switch protocols on or off, pull an IP camera, record or forward a path, or change any MediaMTX
setting. Every save is validated, applied, read back from MediaMTX and kept as a version you can restore.

![The Configuration page with its Quick setup tasks](screenshots/configuration.png)

> [!NOTE]
> **Configuration** is in the menu for admins in **Server** mode (the switch at the top). In **Streaming** mode the
> menu shows only streams and watching.

## Streams and paths

MediaMTX calls every stream a *path*: the name in an address such as `rtmp://your-domain/live/show`. A **stream**
on the **Streams** page is a path that MediaMTX UI manages for you, with its own page, keys, recording switch and
watch link. The **Configuration** page works on paths directly, with every MediaMTX setting. A stream's path shows up
under **Configuration → Paths** too, and the two stay in step.

Paths you add only under **Configuration** have no keys of their own: clients need a
[stream credential](people.md#stream-credentials) for them, or you turn the path into a stream on the **Streams** page.

## The tabs

| Tab | What it is for |
|---|---|
| **Quick setup** | The usual setups, a few questions each |
| **Paths** | Every configured path, and the editor for one path |
| **Global settings** | Settings for the whole server: protocols, ports, timeouts, logging |
| **Path defaults** | What every path gets unless it sets a value of its own |
| **YAML** | The whole file as text |
| **History** | Every version of the file, what changed, and restoring an old one |

## Quick setup

Pick a task, answer its questions, and check **What this changes** (a plain list of the edits) before you press
**Save**. When it is done, the page shows the addresses to use, and a link to **All settings of** the path.

### Re-stream a camera or stream

Pulls a stream from an IP camera or another server and offers it over every protocol that is on.

- **Path name**: what viewers ask for, such as `frontdoor` or `cams/garage`.
- **Pull from**: the source address, for example `rtsp://user:password@192.168.1.20:554/stream1`. Supported are
  `rtsp://`, `rtsps://`, `rtmp://`, `http(s)://` (HLS), `srt://`, `whep(s)://` and `moqt://`. A camera's user name
  and password go into the address.
- **Only connect while someone watches** (on by default): MediaMTX connects to the camera only while someone watches.
  This saves bandwidth and the camera's resources; the first viewer waits a moment.
- **Record it**: also records the path.

Cameras on your local network (such as `192.168.x.x`) are allowed. Addresses inside the stack, loopback and cloud
metadata services are refused.

### Receive a stream from OBS or ffmpeg

Creates a path that an encoder publishes to. Choose what **The publisher uses** (RTSP, RTMP or SRT); if that
protocol is off, the task switches it on too. The result shows the publish address and the watch addresses, and how
to enter them in OBS. The encoder needs a credential with the publish action, or make the path a stream on
**Streams**, which gives it a key. (For most people, creating a stream on **Streams** is the simpler way to do this;
see [Streams](streams.md).)

### Forward a stream to another server

Relays a path to YouTube, Twitch or another server while it is live, and retries when the connection drops. Pick the
path and enter the destination: `rtmp://a.rtmp.youtube.com/live2#<stream key>` for YouTube,
`rtmp://live.twitch.tv/app#<stream key>` for Twitch, or an `rtmp(s)://`, `rtsp(s)://`, `srt://`, `whip(s)://` or
`moqt://` address. The stream key is stored in `mediamtx.yml`. Streams have their own, friendlier forwarding on their
page: see [Forwarding](forwarding.md).

### Record a stream

Records one path, or **Every path (the path defaults)**, in files of **10 minutes**, **1 hour** or **6 hours**, and
keeps them **for a day**, **for a week**, **for 30 days** or **until deleted**. See [Recordings](recordings.md).

### Choose the protocols to serve

Switches MediaMTX's servers on or off: **RTSP**, **RTMP**, **SRT**, **HLS** and **WebRTC**. MediaMTX converts between
them without re-encoding: whatever comes in over one protocol can be read over every protocol that is on. Switch on
only what your encoders and players use. If people are connected to a server you switch off or change, the page warns
you first, for example "Saving restarts the RTMP server: 2 clients will drop and have to reconnect." See
[Protocols and ports](#protocols-and-ports) below.

## Paths

The **Paths** tab lists the paths in `mediamtx.yml` with their source and whether they record. Passwords and stream
keys in source addresses are hidden in the list (the editor shows them). Click a name to edit it, press **New path**
to add one, or **Remove**, then **Remove NAME**, to delete one.

Two kinds of names are special:

- **`all_others`** catches every name that no other path matches. The initial configuration has it, empty, so any
  path name works and the keys or credentials decide who may publish or read where.
- A name starting with **`~`** is a regular expression: `~^cams/(.+)$` matches `cams/garage`, `cams/door` and so on,
  all with the same settings.

Other names use letters, digits and `_ . ~ - /`, not at the start or end.

> [!NOTE]
> This **Paths** tab is about configuration. The **Paths** page in the main menu shows what is live right now: who
> publishes, who reads, and the tracks.

### The path editor

At the top, a new path gets its **name**, and every path its **source**:

- **Default** (whatever the path defaults say; normally `publisher`).
- **Publisher**: clients publish to this path (RTSP, RTMP, SRT, WebRTC).
- **Redirect**: readers are sent to another path or server (set `sourceRedirect` below).
- **Raspberry Pi camera**.
- **Pull from a URL (camera, server, stream)**: MediaMTX connects to the address you enter. Besides the protocols
  above, it takes `rtsp+http://` and similar tunnelled RTSP forms, and `udp+mpegts://` or `udp+rtp://`, where
  MediaMTX listens for incoming UDP.

Below that is every other path setting, grouped as in MediaMTX's own reference configuration, each with its
description. Type in **Filter settings** to find one (by name or by words in its description). A setting left empty
uses the path defaults (shown in grey); yes/no settings offer **Default (…)**, **yes** and **no**. Changed fields get
a dot, and a bar at the bottom counts the **unsaved changes** with **Discard** and **Save** (**Create path** for a new
one). If the path is live, the bar warns that saving restarts it and its readers will reconnect.

Only what you changed is written: if someone else changed the same path meanwhile (another admin, the stream's owner
switching recording, the free-space guard), their change stays.

## Global settings

Every setting for the whole server: logging, timeouts, each protocol's server and address, and so on, with the same
filter, descriptions, and save bar as the path editor. A setting left empty uses MediaMTX's default. Settings shown
with a lock are [locked](#what-is-locked-and-why). Before you save, the bar says which protocol servers restart and
how many clients that drops.

## Path defaults

The same settings as a path, as the starting point for every path that does not set its own value. A change here
reaches every such path, so their clients may reconnect. Two defaults matter in practice:

- `recordPath`: where and under what names recordings are written. It must stay under `/recordings/` and contain
  `%path`, so paths cannot overwrite each other's recordings.
- `recordDeleteAfter`: how long recordings are kept. The initial configuration says `168h` (7 days); `0s` keeps them
  until you delete them. See [Recordings](recordings.md#retention).

## The YAML editor

The **YAML** tab shows the whole file as MediaMTX reads it, for changes that are easier as text (many paths at once,
pasting from MediaMTX's documentation).

- **Check** validates the text without saving. When it passes, the page says "MediaMTX accepts this config."
- **Save** validates and writes it, with the optional **Reason** you typed, which appears in the history. Saving here
  asks you to [confirm it's you](people.md#confirm-its-you).
- **Discard** throws your edits away.

If `mediamtx.yml` changes elsewhere while you edit (another admin, a stream's owner, the server itself), the page
says **Changed meanwhile** and refuses to save, so nobody's change is overwritten unseen. Copy what you need, press
**Load the current file (your edits are discarded)**, and apply your change again.

Comments and layout in the file are kept. The forms on the other tabs also change only the lines they touch.

## History

Every save, from any tab, any stream page or the server itself, becomes a numbered version. The list shows each
version's time, who made it and why (for example `admin: quick setup: protocols`), and marks the **(current)** one.
Click a version to see what it changed compared with the version before it, line by line.

To go back, select a version, press **Restore…**, then **Restore version N** (this asks you to
[confirm it's you](people.md#confirm-its-you)). The old content becomes a new version on top; history is never
rewritten, so you can always undo a restore the same way. A restored version must pass today's checks like any other
change.

The list shows the newest 50 versions. The server keeps roughly the last thousand.

## How every change is checked

MediaMTX stops when it loads an invalid file, and loads its wide-open defaults from an empty one. So the sidecar is the
only thing that writes `mediamtx.yml`, and every change, from any tab, goes through the same steps:

1. **The sidecar's own rules**: the file is not empty, authentication stays with the sidecar, nothing is open to
   anonymous clients, no two servers listen on the same port, MediaMTX's internal servers stay off the published
   stream ports, recordings stay under `/recordings/`, and path names and regular expressions are valid.
2. **MediaMTX's own check**: `mediamtx --validate-conf`, with the same MediaMTX version that runs, catches unknown
   settings, bad values and bad path names.
3. **The write**: only if both pass, the file is replaced in one step and saved as a version.
4. **The read-back**: the sidecar asks MediaMTX for its settings and compares. The page then says, for example,
   "Saved as version 42. MediaMTX applied it." If some settings read back differently, it says which. If MediaMTX
   stops answering after a change (a port already in use, for example), the previous version is put back at once and
   the page tells you to check MediaMTX's log.

If a check fails, nothing is written and the page shows every problem in words, for example
`rtmpAddress and metricsAddress both listen on tcp port 1935`.

**Edits outside the UI.** The sidecar looks at `mediamtx.yml` every two seconds. If someone edited the file directly
and the result is valid, it is recorded as a version by `external`. If the result is invalid, the sidecar replaces it
with the last good version straight away, before MediaMTX can stop over it. Either way, a notice appears on every page
for admins, with **Open the history** and **Dismiss**, and the audit log records it.

## What is locked and why

Some settings keep MediaMTX UI safe and working. **Global settings** shows them with a lock and the reason, and
refuses changes to them:

| Setting | Why |
|---|---|
| `authMethod`, `authHTTPAddress`, `authHTTPExclude`, `authHTTPFingerprint` | MediaMTX asks the sidecar about every connection. Nothing may be excluded: an excluded action would need no credentials at all. |
| `api`, `apiAddress`, `apiEncryption` | The sidecar needs MediaMTX's Control API, on the stack network. |
| `pprof` | Must stay off: it exposes MediaMTX's internals. |
| `hlsAddress`, `hlsEncryption`, `webrtcAddress`, `webrtcEncryption` | The UI's live view reaches MediaMTX's HLS and WebRTC signalling servers here, over the stack network. |

In the YAML editor, the checks refuse any change to the authentication settings, `api` and `pprof`. Leave the
addresses alone there too: the UI would lose its connection to MediaMTX.

This is the **open-relay guard**: MediaMTX's own default configuration lets anyone publish and read, and that can never
reach the file. Watching a public stream without an account works because the sidecar allows it when MediaMTX asks
(read only, never publish), not because the configuration is open.

**Hooks** (the `runOn…` settings, such as `runOnConnect` or `runOnRecordSegmentComplete`) run commands inside the MediaMTX container. No edit
through the UI may add, change or remove one: the forms show them read-only, and a YAML save or a restore that would
change one is refused with a message naming it. Hooks already in the file are left alone.

## Protocols and ports

Clients reach MediaMTX on the stream ports your server publishes. Each protocol you switch on needs its port open in
your firewall (see [Before you start](before-you-start.md)):

| Protocol | Port | Used for | Initially |
|---|---|---|---|
| RTSP | 8554/tcp | Cameras, VLC, ffmpeg, most NVRs | As chosen in the setup wizard |
| RTMP | 1935/tcp | OBS and most streaming software | As chosen in the setup wizard |
| SRT | 8890/udp | Reliable streaming over lossy or long links | As chosen in the setup wizard |
| WebRTC | 8189/udp (media) | Watching in the browser with well under a second of delay, WHIP encoders | On |
| HLS | none of its own | Watching in any browser, a few seconds behind | On |

HLS and WebRTC signalling reach browsers through the UI's own address (port 443), behind its sign-in; only WebRTC's
media needs UDP 8189. Switching HLS off removes the browser player's fallback; switching WebRTC off leaves browsers
with HLS only.

Only these four ports are published. MediaMTX's API, metrics, playback, HLS and WebRTC signalling servers stay on the
stack network, where only the sidecar reaches them. Other listeners MediaMTX offers (RTSPS, RTMPS, RTSP over UDP, MoQ) can be switched
on in **Global settings**, but nothing publishes their ports, so clients outside cannot reach them. If you change a
protocol's port in **Global settings**, the published port does not follow: keep the standard ports.

With [exposure control](exposure-control.md) on, the stream ports are closed until the **Exposure** page or its
automatic rules open them.

## See also

- [Streams](streams.md): the everyday way to set up a stream.
- [Recordings](recordings.md): recording, retention and disk space.
- [Logs and audit](logs-and-audit.md): MediaMTX's log, for when a change did not do what you expected.
- [Backups](backups.md): a backup includes the configuration and its whole history.
