# Changelog

What changed in each release of MediaMTX UI, newest first. Each release's notes on GitHub are its section here.

To upgrade: run the [install script](docs/install-script.md) again, or download the release's `compose.yaml` (and
any add-on file you use, such as `compose.lan.yaml`), then `docker compose pull && docker compose up -d`. Details:
[Upgrading](docs/upgrading.md).

## [Unreleased]

## [1.4.0] - 2026-10-25

### New

- **A stretch of a recording as the holding screen.** On a stream's recording, select up to an hour and press **Use
  as holding screen**: the browser transcodes it into the stream's format and puts it up. See
  [Holding screens](docs/holding-screens.md#a-stretch-of-a-recording).
- **Holding clips up to 3.9 GB and an hour long** (were 64 MB, and two minutes when transcoded in the browser), with
  upload progress. Set the size with `MTXUI_HOLDING_MAX_GB`. MediaMTX needs about 5 s more per hour of clip to load
  its configuration or start the stream.
- **Admins can set a password** for anyone on **People** (Edit, then Set password), including people invited but
  not joined yet. The usual password rules do not apply; it signs the person out everywhere and is audited. See
  [Setting a password](docs/people.md#setting-a-password).
- **Guest keys end to the second.** MediaMTX is told a guest key's expiry when the key is used and ends the
  connection itself at that moment; the sidecar's own check stays as a second line.

### Changed

- **MediaMTX 1.21.3.** Among its fixes: adding, removing or disabling one forward no longer restarts a stream's other
  forwards; recordings are deleted on time even when the configuration changes often; recordings keep their right
  length after packet loss; holding screens accept 7.1 AAC encoders.
- New installs name recording files with their time zone (`%z`, as MediaMTX does by default now). Existing installs
  keep their names, so their recordings stay listed; MediaMTX's new warning about it is harmless (see
  [Troubleshooting](docs/troubleshooting.md#harmless-warnings-in-mediamtxs-log)).
- Release notes come from this file: a release cannot be published without its section here.
- Storing an uploaded holding clip, and checking the clips at start, read the clip about 10 times faster (0.7 s
  instead of 7.6 s for an hour of video).
- **Backups leave out holding clips over 64 MB** (as before, but now they say so). A restore on a server without
  such a clip shows the offline screen on that stream instead of refusing the backup, and its preview names them.
- Behind a reverse proxy: allow request bodies up to 4 GB for holding clips (the examples in
  [Behind a proxy](docs/behind-a-proxy.md) do).

### Fixed

- A two-minute video transcoded into a 1080p holding clip could exceed the old 64 MB limit and was refused.
- Saving a new `logLevel` now warns that it restarts all of MediaMTX's servers (it always did, so encoders and
  viewers drop for a moment); the settings page said it interrupted nothing.

### Security

- **Keys stay out of the log viewer at debug level.** With `logLevel: debug`, MediaMTX 1.21.3 writes the stream key or
  guest key of every connection into its log file. The log viewer, its live view and the download hide them, and the
  settings page and the Logs page warn while debug is on. See [Logs](docs/logs-and-audit.md#mediamtxs-log-level-and-rotation).
- The four HTTP/2 advisories tolerated for MediaMTX 1.21.2's binary are fixed in 1.21.3.
- **Go 1.27.3** for the sidecar: Go's certificate checks distrust compromised intermediate certificates.
- zod 4.6.7 in the web app: validating an oversized list could exhaust the browser tab's memory. Vite, Vitest and the
  shadcn tool, used only to build and test, are updated past their advisories too.

## [1.3.0] - 2026-10-12

### New

- **See exactly what you export.** The recording page shows the **first and last frame** of the selection, with their
  times, so you see where an export begins and ends before you play or download it.
- **Handles on the selection.** Drag either end, or click a handle and use the arrow keys: a second per press, ten
  with Shift, a minute with Page Up and Page Down. A new **1 min** zoom places them to the second.
- **Streamers see their own streams' recordings**: they can play and download them; deleting stays with operators and
  admins.

### Security

- **MediaMTX 1.21.2.** Among its fixes: a crafted WebRTC watch request could crash MediaMTX; kicking RTSP sessions
  had a race; it is built with Go 1.27.2.
- **Go 1.27.2** for the sidecar: eleven fixes in Go's standard library, among them HTTP/2 crashes and memory
  exhaustion, unbounded Range headers and HTTP/1 desynchronisation.
- **Forwards may go to addresses on your local network** (another server or recorder next door). The server itself,
  its internal network and cloud metadata addresses stay refused.

### Fixed

- Playback on iPhone and Safari no longer stops after a while, and Tapo and TP-Link cameras record cleanly (MediaMTX
  1.21.2).

## [1.2.2] - 2026-10-04

### Fixed

- Install script: answering its yes/no question no longer ends it with an error.

## [1.2.1] - 2026-10-04

### Fixed

- Install script: run as `curl … | sudo bash`, it no longer stops silently after "Running"; it prints the address
  and the setup token.

### New

- Install script: checks memory, free memory, disk and CPU before installing, and offers a swap file on a small
  machine without one.

## [1.2.0] - 2026-10-03

### New

- **Install script**: one command from a fresh Linux machine to a running setup page, Docker included; run it again
  to upgrade. See [Install script](docs/install-script.md).

## [1.1.0] - 2026-10-03

The first public release: streams with their own pages and keys, live view and multi-view, recordings, forwarding,
holding screens, people and roles with second factors, configuration of MediaMTX with history, logs, audit log and
encrypted backups; HTTPS from Let's Encrypt built in, or a local network only.

[Unreleased]: https://github.com/greatmastix/MediaMTXUI/compare/v1.4.0...HEAD
[1.4.0]: https://github.com/greatmastix/MediaMTXUI/compare/v1.3.0...v1.4.0
[1.3.0]: https://github.com/greatmastix/MediaMTXUI/compare/v1.2.2...v1.3.0
[1.2.2]: https://github.com/greatmastix/MediaMTXUI/compare/v1.2.1...v1.2.2
[1.2.1]: https://github.com/greatmastix/MediaMTXUI/compare/v1.2.0...v1.2.1
[1.2.0]: https://github.com/greatmastix/MediaMTXUI/compare/v1.1.0...v1.2.0
[1.1.0]: https://github.com/greatmastix/MediaMTXUI/releases/tag/v1.1.0
