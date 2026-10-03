# MediaMTX UI documentation

Everything about installing, using and running MediaMTX UI, from a server that has never seen Docker to backups,
upgrades and security. If you are new to servers, follow **Getting started** in order; each page says what to do,
why, and what you should see when it worked.

New here? Read [Before you start](before-you-start.md) first: it helps you choose where to run it and what you need.

## Getting started

| Page | What it covers |
|---|---|
| [Before you start](before-you-start.md) | Choosing a server or a Raspberry Pi, a domain name, which ports to open |
| [Install Docker](install-docker.md) | Docker and Docker Compose on Ubuntu, Debian, Raspberry Pi OS and others, from zero |
| [Install MediaMTX UI](install.md) | The public install with HTTPS from Let's Encrypt, step by step |
| [Local network only](install-local.md) | A home or studio network that the internet cannot reach |
| [First steps](first-steps.md) | The setup wizard, your first stream, streaming from OBS, watching |

## Streaming

| Page | What it covers |
|---|---|
| [Streams](streams.md) | Creating streams, keys, public and private streams, guest keys |
| [Encoders](encoders.md) | OBS, ffmpeg, IP cameras, phones; which protocol to choose |
| [Watching](watching.md) | The player, the multi-view grid, watch links, external players |
| [Forwarding](forwarding.md) | Sending a stream on to YouTube, Twitch and other servers |
| [Holding screens](holding-screens.md) | A clip that plays while nobody is streaming |

## Administration

| Page | What it covers |
|---|---|
| [People and access](people.md) | Roles, invitations, passwords, authenticator apps, passkeys |
| [Configuration](configuration.md) | MediaMTX's settings: quick setup, paths, the YAML editor, history |
| [Recordings](recordings.md) | Recording, playing, downloading, storage limits |
| [Logs and audit](logs-and-audit.md) | MediaMTX's log, the UI's log, the audit log of every change |

## Running a server

| Page | What it covers |
|---|---|
| [Upgrading](upgrading.md) | Moving to a new release |
| [Backups and restore](backups.md) | Encrypted backups, restoring, moving to a new server |
| [Behind a reverse proxy](behind-a-proxy.md) | Caddy, nginx or Traefik in front of the UI |
| [Exposure control](exposure-control.md) | Stream ports that stay closed until needed (advanced) |
| [All settings](config.md) | Every environment variable of the sidecar |
| [Troubleshooting](troubleshooting.md) | Symptoms, causes and fixes; what each warning means |
| [Uninstall](uninstall.md) | Removing it again, with or without your data |

## Reference

| Page | What it covers |
|---|---|
| [Security](security.md) | What it defends against, how, and the tests behind each item |
| [FAQ](faq.md) | Short answers to common questions |
| [Glossary](glossary.md) | Docker, ports, RTMP, WebRTC and the other words these pages use |
| [MediaMTX API coverage](api-coverage.md) | Which MediaMTX API calls the UI proxies, and for which role (for developers) |

Found a mistake, or something missing? Open an issue on
[GitHub](https://github.com/greatmastix/MediaMTXUI/issues). Security problems: see [SECURITY.md](../SECURITY.md).
