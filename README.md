<div align="center">

# MediaMTX UI

**A web UI for [MediaMTX](https://github.com/bluenviron/mediamtx): run your own streaming server, from one Docker Compose file.**

Streams with their own pages and keys, a live view and multi-view, recordings, users and roles, forwarding to other
platforms, holding screens, logs and backups. HTTPS with Let's Encrypt is built in.

[![CI](https://github.com/greatmastix/MediaMTXUI/actions/workflows/ci.yml/badge.svg)](https://github.com/greatmastix/MediaMTXUI/actions/workflows/ci.yml)
[![Release](https://github.com/greatmastix/MediaMTXUI/actions/workflows/release.yml/badge.svg)](https://github.com/greatmastix/MediaMTXUI/actions/workflows/release.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

![The dashboard](docs/screenshots/dashboard.png)

</div>

## Install

**The quick way**, on a fresh Ubuntu, Debian or Raspberry Pi OS machine: one command installs Docker if needed, asks
whether the server is on the internet (with a domain) or on your local network only, and starts everything:

```bash
curl -fsSL https://github.com/greatmastix/MediaMTXUI/releases/latest/download/install.sh | sudo bash
```

It prints the address to open and the setup token. Run it again later to upgrade. Details:
[Install script](docs/install-script.md).

**Step by step** instead:

> [!TIP]
> **New to servers or Docker?** The [documentation](docs/README.md) starts from zero: [choosing a
> server](docs/before-you-start.md), [installing Docker](docs/install-docker.md), then [the install step by
> step](docs/install.md) with what you should see at each step. On a home or studio network without a domain:
> [Local network only](docs/install-local.md).

You need a Linux host with Docker (with the Compose plugin), and a domain name pointing at it (an A record: see
[Ports](#ports)).

**1. Get the two files** of the latest release

```bash
mkdir mediamtx-ui && cd mediamtx-ui
curl -fsSLO https://github.com/greatmastix/MediaMTXUI/releases/latest/download/compose.yaml
curl -fsSL -o .env https://github.com/greatmastix/MediaMTXUI/releases/latest/download/env.example
```

**2. Set your domain** in `.env` (and an e-mail address for Let's Encrypt, if you like):

```bash
DOMAIN=stream.example.com
ACME_EMAIL=you@example.com
```

**3. Start it**

```bash
docker compose up -d
```

**4. Open `https://stream.example.com`** and create the first admin with the setup token:

```bash
docker compose exec sidecar /mtxui setup-token
```

That's it. The certificate is requested on the first start and renewed by itself. The setup wizard switches on the
protocols you want; then create a stream on the **Streams** page and point your encoder (OBS, ffmpeg, a camera) at
the address and key it shows.

> [!TIP]
> Trying it out? Add `ACME_DIRECTORY=https://acme-staging-v02.api.letsencrypt.org/directory` to `.env` to use
> Let's Encrypt's staging server, which has generous rate limits (browsers will warn about its certificate).
> To switch to the real certificate, remove the line and run `docker compose up -d` again.

### Ports

Open these in your firewall or cloud provider's security group:

| Port | Protocol | For |
|---|---|---|
| 80, 443 | TCP | The UI (HTTPS), and Let's Encrypt's check of your domain |
| 1935 | TCP | RTMP: OBS and most encoders |
| 8554 | TCP | RTSP: cameras and players |
| 8890 | UDP | SRT |
| 8189 | UDP | WebRTC: watching in the browser, WHIP encoders |

Only open the stream ports you use. MediaMTX's API, metrics and playback server are never published: the sidecar
answers for them, behind its sign-in.

The ports are published on the host's IPv4 addresses only (`0.0.0.0`), so the domain needs an A record. An AAAA
record alone does not work, and next to an A record it sends IPv6 clients to a closed port first. Publishing on IPv6
(`[::]`) is not supported: the stack's Docker network has no IPv6, so Docker would relay every IPv6 connection to
the containers from the network's gateway, and sign-in limits and address rules could no longer tell those clients
apart.

## Features

- **Streams** with their own page, keys and encoder settings (OBS presets, health checks while you are live), owned by
  streamer accounts that see only their own streams. New streams are public, with a watch link anyone can open;
  the stream's owner, an operator or an admin can make one private.
- **Live view** in the browser over WebRTC, with an HLS fallback, a multi-view grid with saved layouts, and live
  charts of bandwidth and viewers (an hour live, 30 days of history).
- **Recordings** on a timeline per stream: play, download a range as MP4, delete. A storage budget deletes the
  oldest, and a free-space guard stops recording before the disk fills up.
- **Forwarding** to YouTube, Twitch and other servers, and **holding screens** that play while nobody streams (your
  own clip, transcoded in the browser, or a built-in "offline" screen).
- **Guest keys**: let someone stream or watch for a while, without an account.
- **Configuration** of MediaMTX with guided setups and every setting, a raw YAML editor and the full history of every
  change. Every config is validated before MediaMTX sees it, and edits touch only the lines they change.
- **People and roles** (admin, operator, viewer, streamer), invitations by one-time join code, authenticator apps and
  passkeys, "confirm it's you" before admin-level changes, and an **audit log** of everything anyone changed.
- **Logs** of MediaMTX and the UI, live and searchable, and **backups**: encrypted with your passphrase, daily and on
  demand, restorable on a fresh server.
- Runs on **amd64 and ARM** (arm64, armv7: a Raspberry Pi will do).

| | |
|---|---|
| ![Multi-view](docs/screenshots/watch.png) | ![A stream's page](docs/screenshots/stream.png) |
| ![Configuration](docs/screenshots/configuration.png) | ![Logs](docs/screenshots/logs.png) |

## Documentation

The [documentation](docs/README.md) covers everything in detail:

- **Getting started**: [Before you start](docs/before-you-start.md), [Install Docker](docs/install-docker.md),
  [Install](docs/install.md), [Local network only](docs/install-local.md), [First steps](docs/first-steps.md)
- **Streaming**: [Streams](docs/streams.md), [Encoders](docs/encoders.md) (OBS, ffmpeg, cameras),
  [Watching](docs/watching.md), [Forwarding](docs/forwarding.md), [Holding screens](docs/holding-screens.md)
- **Administration**: [People and access](docs/people.md), [Configuration](docs/configuration.md),
  [Recordings](docs/recordings.md), [Logs and audit](docs/logs-and-audit.md)
- **Running a server**: [Upgrading](docs/upgrading.md), [Backups](docs/backups.md),
  [Behind a reverse proxy](docs/behind-a-proxy.md), [All settings](docs/config.md),
  [Troubleshooting](docs/troubleshooting.md), [FAQ](docs/faq.md)

## Upgrading

Download the new release's `compose.yaml` (and any override you use), then recreate the stack:

```bash
curl -fsSLO https://github.com/greatmastix/MediaMTXUI/releases/latest/download/compose.yaml
docker compose pull && docker compose up -d
```

The database migrates by itself. Never upgrade one image alone: each release's `compose.yaml` names the sidecar and
the MediaMTX it was tested with. Details, and how to go back: [Upgrading](docs/upgrading.md).

## Backups

Set a passphrase on the **Backups** page: from then on a backup is made every night, encrypted with it. Download one
now and then to keep a copy off the server, and keep the passphrase safe: without it, nobody can open a backup.
Restoring and moving to a new server: [Backups](docs/backups.md).

## Security

- Nothing anonymous: MediaMTX asks the sidecar about every connection (`authMethod: http`), and its open-relay
  defaults never reach the config. Publishing always takes a key. Watching without an account takes one only for a
  private stream: streams are public when they are created, and the stream's owner, an operator or an admin can make
  one private (and public again).
- Both containers run as an unprivileged user on a read-only filesystem with every capability dropped. The sidecar
  holds no host credentials and does not see the Docker socket.
- Secrets are generated on the first start and kept in the state volume; none goes into `.env` or the compose file.
- Passwords are hashed with argon2id; sign-in is rate-limited and locks out guessing; sessions are server-side.
- Optional second factors (authenticator app, passkeys), and a fresh confirmation before admin-level changes.

[docs/security.md](docs/security.md) has the whole list, with the test or check behind each item, and the
decisions to know about. Found a vulnerability? See [SECURITY.md](SECURITY.md).

**Exposure control** (advanced): stream ports that stay closed except when they are needed. Admins open them for an
address or a while, and automatic rules (all on by default) let an encoder's address in while its stream page is open,
and open the ports to anyone while a stream is live or has a holding screen, or a guest publish key is valid. It needs
a firewall in front of Docker that ufw rules control; see [docs/exposure-control.md](docs/exposure-control.md).

## Troubleshooting

- **No certificate / the browser warns**: `docker compose logs sidecar` says why. Usually the domain does not point at
  this host yet, or ports 80 and 443 are not reachable from the internet.
- **The encoder cannot connect**: is the stream port open in the firewall, and is the protocol on (Configuration →
  Quick setup → Choose the protocols to serve)? The stream's page says what it sees, and **Logs** shows MediaMTX's side.
- **The browser plays nothing, or only after a while**: WebRTC needs UDP 8189; without it the player falls back to
  HLS. Encoders that send B-frames (OBS's x264 default) can only be watched over HLS.
- **Lost the only admin's password**: `docker compose exec sidecar /mtxui reset-password --username NAME`, then open
  `/join` and enter the code it prints.

Everything else, by symptom, and what each warning in the UI means: [Troubleshooting](docs/troubleshooting.md).

## Development

Everything runs in pinned containers: you need bash and Docker. `./dev up` starts a dev stack on `127.0.0.1`,
`./dev ci` runs the checks and `./dev e2e` the end-to-end tests. [CONTRIBUTING.md](CONTRIBUTING.md) has the details.

## License

[MIT](LICENSE). MediaMTX UI is not affiliated with the MediaMTX project; it runs the official
[MediaMTX](https://github.com/bluenviron/mediamtx) image (MIT) alongside its own.
