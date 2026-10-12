# Changelog

What changed in each release of MediaMTX UI, newest first. Each release's notes on GitHub are its section here.

To upgrade: run the [install script](docs/install-script.md) again, or download the release's `compose.yaml` (and
any add-on file you use, such as `compose.lan.yaml`), then `docker compose pull && docker compose up -d`. Details:
[Upgrading](docs/upgrading.md).

## [Unreleased]

### Changed

- Release notes come from this file: a release cannot be published without its section here.

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

[Unreleased]: https://github.com/greatmastix/MediaMTXUI/compare/v1.3.0...HEAD
[1.3.0]: https://github.com/greatmastix/MediaMTXUI/compare/v1.2.2...v1.3.0
[1.2.2]: https://github.com/greatmastix/MediaMTXUI/compare/v1.2.1...v1.2.2
[1.2.1]: https://github.com/greatmastix/MediaMTXUI/compare/v1.2.0...v1.2.1
[1.2.0]: https://github.com/greatmastix/MediaMTXUI/compare/v1.1.0...v1.2.0
[1.1.0]: https://github.com/greatmastix/MediaMTXUI/releases/tag/v1.1.0
