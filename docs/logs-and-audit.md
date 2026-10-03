# Logs and audit

When something does not work, the logs say why; when something changed, the audit log says who did it. This page
covers the **Logs** page (MediaMTX's log and the sidecar's), what the common MediaMTX messages mean, reading the logs
on the server, the **Audit log** page, and how long chart history is kept. Both pages are for admins (in **Server**
mode).

**Short version:** an encoder cannot connect? Open **Logs**, keep **Follow live** on, and connect it: MediaMTX's
lines show what it saw. Switch **Log** to **Sidecar** to see why a key was refused. **Audit log** lists every change
with who, from where and when.

![The Logs page](screenshots/logs.png)

## The Logs page

At the top:

- **Log**: **MediaMTX** (the streaming server: connections, publishing, reading, recording, errors) or **Sidecar**
  (MediaMTX UI itself: sign-ins, key checks, config writes, certificates).
- **Level**: **Everything**, **Info and above**, **Warnings and errors** or **Errors**.
- **Containing**: a word, a path, an address (up to 200 characters, upper and lower case alike). Press **Show** to
  apply it.
- **Follow live** / **Stop following**.
- **Download** (MediaMTX's log only).

Times are shown in your own browser's time zone. Warnings are coloured, errors red.

**Following live** (the default) starts with the newest 300 matching lines and adds new ones as they are written,
staying scrolled to the bottom unless you scroll up. The view keeps the newest 2,000 lines. **Stop following** turns
it into a search: up to the newest 2,000 matches, including MediaMTX's older, rotated log files. If there were more,
the page says so; narrow the search to see older ones.

**Download** saves MediaMTX's whole log, rotated copies included, oldest first, as one text file. MediaMTX writes some
text that clients send (a rejected path name, for example) into its log as is, so a line in the file can be forged
by a client. Read the file with a viewer that escapes control characters, such as `less`.

At most 32 live views can be open at once across all admins; beyond that the page says "Too many open log views.
Close some tabs."

### MediaMTX's log level and rotation

MediaMTX logs at `info` by default. For more detail while you track down a problem, set `logLevel` to `debug` under
**Configuration → Global settings** (and back to `info` afterwards: debug is very chatty).

MediaMTX's log file is rotated so it cannot fill the disk: when it grows past 20 MB it is compressed and a fresh one
started, and the 5 newest old copies are kept. The search reads them all. Change this with
`MTXUI_MEDIAMTX_LOG_MAX_MB` (1 to 1024) and `MTXUI_MEDIAMTX_LOG_KEEP` (0 to 50) in a `compose.override.yaml`; see
[Sidecar configuration](config.md) and the example in [Recordings](recordings.md#lowering-the-limits).

### The sidecar's log

The **Sidecar** log shows the newest 5,000 lines since the sidecar last started; they are kept in memory only. Its
level is `info`; to change it, set `LOG_LEVEL` in `.env` to `debug`, `info`, `warn` or `error` and run
`docker compose up -d`.

## Common MediaMTX messages

MediaMTX writes each line as the server, the connection, and what happened, for example:

```
INF [RTMP] [conn 203.0.113.7:51234] opened
INF [RTMP] [conn 203.0.113.7:51234] is publishing to path 'live/show', 2 tracks (H264, MPEG-4 Audio)
```

| You see | What it means | What to do |
|---|---|---|
| `[conn …] opened`, then `closed: …` | A client connected; the end of the line says why it left. | Read the reason. |
| `is publishing to path 'NAME', 2 tracks (H264, MPEG-4 Audio)` | An encoder is live on that path, with those tracks. | Nothing: this is success. |
| `is reading from path 'NAME', …` | A player is watching. | Nothing. |
| `failed to authenticate: server replied with code 401` | The sidecar refused the client's key or credential, or the client sent none for a private stream. | Check the stream key in the encoder. The **Sidecar** log has an `auth: denied` line with the reason, and the stream page's checks help too. |
| `no stream is available on path 'NAME'` | A player asked for a path nobody is streaming to (yet). | Start the encoder, or check the path name. |
| `closed: WebRTC doesn't support H264 streams with B-frames` | The encoder sends B-frames, which browsers cannot play over WebRTC. The player falls back to HLS, a few seconds behind. | Switch B-frames off in the encoder; see [Encoders](encoders.md). |
| `closed: wants to publish [H264 Opus], but stream expects [H264 MPEG-4 Audio]` | The stream has a holding screen, and the encoder's tracks do not match the holding clip's. | The stream page explains it; see [Holding screens](holding-screens.md). |
| `closed: MPEG-4 audio configuration does not match, …` | Same cause, for the audio format (sample rate or channels). | As above. |
| `closed: invalid path name …` | A client used a path name MediaMTX does not accept, often a scanner on the internet. | Nothing, unless it is your own encoder: then check the address. |

On the **Sidecar** side, the lines that matter most for streaming:

| You see | What it means |
|---|---|
| `auth: denied … reason="unknown credential or wrong secret"` | A key or credential that does not exist, or the wrong secret. The line names the address, action, path and protocol (never the secret). |
| `auth: throttling address after repeated failures` | An address sent wrong keys 20 times; its attempts are refused for 5 minutes. A misconfigured encoder retrying can cause this. |
| `username locked after repeated failed sign-ins` | Too many failed sign-ins (5 by default) for one username from one address; see [Sign-in protection](people.md#sign-in-protection). |

## Reading the logs on the server

The **Logs** page needs a working sign-in. When it is not available (the UI does not start, no certificate yet),
read the containers' output on the server, in the folder with `compose.yaml`. The sidecar's log, which also shows
certificate problems and the setup token message:

```bash
docker compose logs sidecar
```

The newest 100 lines, then new ones as they come (press Ctrl+C to stop):

```bash
docker compose logs -f --tail 100 sidecar
```

MediaMTX's output, the same way:

```bash
docker compose logs mediamtx
```

Docker keeps up to three files of 10 MB per container. See [Troubleshooting](troubleshooting.md) for what to look
for.

## The Audit log page

The audit log records every change anyone makes, and what the server does by itself: one entry per change, with
**When**, **Who** (and from which address), **What**, **On** what, and **Details**. It can only grow: nobody, admins
included, can edit or delete entries.

It records, among other things:

- signing in and out, failed and locked sign-ins, joining with a code, "Confirm it's you";
- changes to your own account (password, authenticator app, passkeys, sessions);
- people: invitations, join and reset codes, roles, disabling, deleting, second-factor resets;
- credentials created and revoked; stream keys shown or regenerated;
- streams: created, changed, deleted, disconnected, forwarding, guest keys, holding clips;
- every configuration change, with its version number, including refused ones and why they were refused;
- recordings deleted by hand or by the budget, the free-space guard;
- exposure changes, backups and restores;
- what the server did by itself, as `system`: outside edits of `mediamtx.yml`, automatic deletions.

Secrets never go into it. Commands run on the server (`/mtxui reset-password`, `/mtxui credential …`) appear as
`cli`; sign-in attempts before anyone is signed in appear as `anonymous`.

### Filters

- **Who**: a username, exactly (upper and lower case alike), or `system`.
- **What**: **Everything**, **Signing in and confirming**, **Own account changes**, **People**, **Credentials**,
  **Streams**, **Configuration**, **Exposure** or **Recordings**.
- **On**: part of the target's name: a stream, a person, a path.
- **From** and **Until**: a time range.

Press **Show** to apply them. The newest 100 entries come first; **Load more** pages back.

### Export

**Export CSV** and **Export JSON** download what the current filter matches, up to 10,000 entries, for a spreadsheet
or another tool. The CSV has the columns `id`, `at` (UTC), `actor`, `ip`, `action`, `target` and `details` (as JSON).

The audit log is part of every [backup](backups.md).

## Chart history

The charts on the **Dashboard** and on stream pages (bandwidth, viewers, streams) show the last hour live, and longer
ranges (**24 h**, **7 d**, **30 d**) from a per-minute history. That history is kept for 30 days by default. To keep
more or less (1 to 365 days), add this line to `.env`:

```
HISTORY_DAYS=90
```

Then apply it:

```bash
docker compose up -d
```

The charts never show more than 30 days; keeping a shorter history makes the longer ranges partly empty. Chart history
is included in backups.

## See also

- [Troubleshooting](troubleshooting.md): what to do about the problems the logs show.
- [People and access](people.md): roles, sign-in protection, "Confirm it's you".
- [Sidecar configuration](config.md): every setting mentioned here.
