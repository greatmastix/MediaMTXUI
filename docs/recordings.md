# Recordings

MediaMTX can save what is streamed to disk. This page explains how to switch recording on, how to find, play,
download and delete recordings, and how MediaMTX UI keeps them from filling your disk: the storage budget, the
free-space limits, keeping recordings on another disk, and how long they are kept. Turning recording on is for a
stream's owner, operators and admins; the disk settings are for whoever runs the server.

**Short version:** tick **Record this stream** on a stream's page. Recordings appear under **Recordings**, on a
timeline per stream, ready to play or download as MP4. They are kept for 7 days by default. On a small disk (a
Raspberry Pi's SD card, a small VPS), [lower the free-space limits](#free-space-limits) first.

## Turning recording on

Recording happens only while something is streamed to the path. There are three ways to switch it on:

- **One stream**: on the stream's page, under **Settings**, tick **Record this stream** and press **Save**. The
  stream's owner, operators and admins can do this. What is recorded is what the encoder sends, not the holding
  screen.
- **One path** (admins): **Configuration → Paths**, open the path, set `record` to **yes** and **Save**. Or use the
  Quick setup task **Record a stream**, which also asks for the file length and how long to keep them.
- **Every path** (admins): set `record` to **yes** under **Configuration → Path defaults**, or pick **Every path (the
  path defaults)** in the **Record a stream** task. Each path can still say no for itself.

MediaMTX writes recordings in segments: files of a fixed length (`recordSegmentDuration`, one hour unless you choose
otherwise in the Quick setup task). See [Configuration](configuration.md) for the path settings.

## Finding recordings

The **Recordings** page (viewers, operators and admins) starts with the **Recordings disk** card, then lists every
path that has recordings, with their **Size**, number of **Segments**, when they start (**From**) and the **Latest**
one. Click a path to open its timeline. A stream's page also links to its **Recordings**.

> [!NOTE]
> Streamers do not see the **Recordings** page, even for their own streams. A viewer, operator or admin can download
> a recording for them.

## The timeline

The timeline shows what was recorded as bars; gaps are times nothing was recorded. Small ticks above the bars mark
where each segment starts.

- **Earlier** and **Later** (the arrow buttons) move the window by half its width; **24 h**, **6 h**, **1 h** and
  **10 min** zoom; **Latest** jumps to the newest recording.
- **Drag** across the timeline to select a stretch, **click** to select a minute, or **double-click** a bar to select
  all of that recorded stretch.
- Or type the start into **From** and the **Length (seconds)**.

Below the timeline the page sums up the selection: date, start and end time, length, and how many segments start in
it.

## Playing and downloading

With a stretch selected:

- **Play** plays it right on the page.
- **Download MP4** saves it as one MP4 file, which plays in VLC, QuickTime, video editors and most other players.

Both have limits, so that one long download cannot tie up the server:

| Limit | Default | Setting |
|---|---|---|
| Longest stretch per play or download | 2 hours | `MTXUI_EXPORT_MAX_DURATION` (1m to 24h) |
| Largest download (the file stops there) | 8 GB | `MTXUI_EXPORT_MAX_GB` |
| Plays and downloads running at once, for everyone together | 2 | `MTXUI_EXPORT_CONCURRENCY` (1 to 16) |

A longer selection says "Play and download take at most 2h 0m at a time: select less, or download it in parts." When
all slots are busy, the server answers that exports are running; try again when one ends. These settings go into a
`compose.override.yaml`, the same way as the [free-space limits](#free-space-limits).

## Deleting

Operators and admins see **Delete N segments** for a selection. Confirm with **Delete**. A segment is deleted whole,
so a little past the end of the selection may go too, and it cannot be undone. Deletion goes through MediaMTX, and
the audit log records it.

## Storage budget

A budget caps how much space recordings may take. Over it, the oldest segments are deleted, just enough of them,
across all paths. To set one, add this line to `.env` next to `compose.yaml` (here, 200 GB; decimals are allowed):

```
RECORDINGS_MAX_GB=200
```

Then apply it:

```bash
docker compose up -d
```

`0`, the default, means no budget: then only the free-space limits below apply. The **Recordings disk** card shows
the budget next to the space recordings take.

The newest segment of each path is never deleted, since it may still be recording. Files in the recordings folder
that MediaMTX does not list (left from an earlier `recordPath`, or copied there) count toward the budget but are
never deleted; if they alone keep recordings over the budget, a banner says so, and you remove them by hand.

## Free-space limits

Whatever the budget, two limits protect the disk:

- **Below 20 GB free**, the oldest segments are deleted until there is 20 GB free again.
- **Below 5 GB free**, recording is switched off for every path that records. A banner on every page and the
  **Recordings disk** card say **Recording is switched off**, and the audit log records it. Make room, then an admin
  presses **Switch recording back on** on the **Recordings** page. That works once 20 GB are free again.

The space counted is the free space of the disk the recordings are on. With the standard install, that is the disk
Docker keeps its data on, which is usually your system disk. The card shows both limits as **Oldest go when** and
**Recording stops**. The server checks every minute (`MTXUI_RECORDINGS_CHECK_EVERY`), and records each automatic
deletion in the audit log (as `system`). If deleting every recording would still not free enough space, it deletes
none and a banner says that something other than recordings fills the disk.

> [!WARNING]
> On a disk with less than 20 GB free, the defaults delete recordings as soon as they are made, and below 5 GB nothing
> is recorded at all. On a small disk, lower both limits.

### Lowering the limits

Sidecar settings that `.env` does not cover go into a file called `compose.override.yaml`, next to `compose.yaml`.
(Never edit `compose.yaml` itself: an upgrade replaces it.) For example, to keep 4 GB free and stop recording below
1 GB, create `compose.override.yaml` with exactly this content:

```yaml
services:
  sidecar:
    environment:
      MTXUI_RECORDINGS_MIN_FREE_GB: "4"
      MTXUI_RECORDINGS_CRITICAL_FREE_GB: "1"
```

The second number must be lower than the first (unless the first is 0). `MTXUI_RECORDINGS_MIN_FREE_GB: "0"` deletes
nothing for free space, and `MTXUI_RECORDINGS_CRITICAL_FREE_GB: "0"` never stops recording; with both at zero, only the
budget protects the disk.

Docker Compose reads `compose.override.yaml` by itself, unless `.env` sets `COMPOSE_FILE` (as it does for
[local-network mode](install-local.md) or [behind a proxy](behind-a-proxy.md)). Then add the file to that list, for
example:

```
COMPOSE_FILE=compose.yaml:compose.lan.yaml:compose.override.yaml
```

Apply the change:

```bash
docker compose up -d
```

The sidecar restarts with the new limits; MediaMTX keeps running. If a value is invalid, the sidecar does not start,
and its log names the problem:

```bash
docker compose logs sidecar
```

## Keeping recordings on another disk

By default, recordings live in a Docker volume on Docker's disk. To keep them on a disk of your choice (a large USB
drive, a second disk), point `RECORDINGS_PATH` at a folder there.

1. Create the folder, owned by the user the containers run as (uid 10002), for example on a disk mounted at `/srv`:

   ```bash
   sudo install -d -o 10002 -g 10002 -m 750 /srv/recordings
   ```

2. Add this line to `.env`:

   ```
   RECORDINGS_PATH=/srv/recordings
   ```

3. Recreate the stack:

   ```bash
   docker compose up -d
   ```

From then on, new recordings go to `/srv/recordings`, and the free-space limits measure that disk. Recordings already
in the Docker volume are not moved and no longer appear on the **Recordings** page. The path must be absolute.

## Retention

Besides the budget and the free-space limits, MediaMTX deletes recordings by age: `recordDeleteAfter` under
**Configuration → Path defaults**. The initial value is `168h` (7 days). Examples: `24h` for a day, `720h` for 30
days, `0s` to keep recordings until you delete them (or the budget or free space does). A path can set its own value
in the path editor, and the Quick setup task **Record a stream** sets it together with recording.

## See also

- [Sidecar configuration](config.md): every recording and export setting.
- [Backups](backups.md): backups do not include recordings; copy those yourself if you need them kept.
- [Troubleshooting](troubleshooting.md).
