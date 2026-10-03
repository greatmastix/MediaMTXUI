# Frequently asked questions

Short answers to the questions people ask most, with links to the pages that explain more. For something that does
not work, see [Troubleshooting](troubleshooting.md).

**About the project**

- [What does it cost? What is the license?](#what-does-it-cost-what-is-the-license)
- [Is it part of the MediaMTX project?](#is-it-part-of-the-mediamtx-project)
- [Is it secure?](#is-it-secure)

**Running it**

- [Can I run it on a Raspberry Pi?](#can-i-run-it-on-a-raspberry-pi)
- [Can I run it on Windows or macOS?](#can-i-run-it-on-windows-or-macos)
- [Do I need a domain name?](#do-i-need-a-domain-name)
- [Can I use it only on my local network?](#can-i-use-it-only-on-my-local-network)
- [Can it be reached by more than one domain?](#can-it-be-reached-by-more-than-one-domain)
- [Can I change the domain later?](#can-i-change-the-domain-later)
- [Does it work with IPv6?](#does-it-work-with-ipv6)
- [Can I put it behind Cloudflare's proxy?](#can-i-put-it-behind-cloudflares-proxy)
- [Where is my data?](#where-is-my-data)
- [How do I reset everything and start over?](#how-do-i-reset-everything-and-start-over)

**Streaming**

- [Does it transcode?](#does-it-transcode)
- [How many viewers can watch?](#how-many-viewers-can-watch)
- [How much delay is there?](#how-much-delay-is-there)
- [Which OBS settings should I use?](#which-obs-settings-should-i-use)
- [Can I stream to YouTube or Twitch at the same time?](#can-i-stream-to-youtube-or-twitch-at-the-same-time)
- [Can I import my existing MediaMTX configuration?](#can-i-import-my-existing-mediamtx-configuration)

## What does it cost? What is the license?

Nothing. MediaMTX UI is open source under the [MIT license](../LICENSE): you may use, change and share it freely,
also commercially. What you pay for is your own server (or Raspberry Pi), a domain name if you use one, and the
internet bandwidth your streams use.

## Is it part of the MediaMTX project?

No. MediaMTX UI is an independent project and is not affiliated with the [MediaMTX](https://github.com/bluenviron/mediamtx)
project. It runs the official, unmodified MediaMTX image next to its own container, the sidecar, which adds the web
UI, accounts and everything else. Problems with MediaMTX UI belong in
[this project's issues](https://github.com/greatmastix/MediaMTXUI/issues), not MediaMTX's.

## Is it secure?

It is built to be safe on the internet. Publishing always needs a key, MediaMTX's control API is never published,
passwords are stored hashed, sign-in limits guessing, and admin-level changes ask you to confirm it's you. Both
containers run without privileges on read-only filesystems. One thing to know: new streams are public, meaning
anyone with the watch link can watch them (never stream to them); you can make a stream private. The full list,
with the test behind each item, is on [Security](security.md).

## Can I run it on a Raspberry Pi?

Yes. The images run on 64-bit ARM (arm64) and 32-bit ARM (armv7), as well as on ordinary PCs and servers (amd64). That
covers the Raspberry Pi 2, 3, 4 and 5 and the Zero 2 W, but not the original Pi Zero or Pi 1. Two things to keep in
mind: a Pi at home needs port forwarding on your router to be reachable from the internet, and its SD card is small,
so lower the recordings free-space limits (see [Recordings](recordings.md)). See also
[Before you start](before-you-start.md).

## Can I run it on Windows or macOS?

It is made for a Linux host, and that is what it is tested on. Docker Desktop on Windows or macOS runs containers
inside a hidden Linux virtual machine, which changes how network ports and client addresses behave; it is not tested
there. For a real install, use a Linux machine: a small cloud server (VPS), a spare PC with Ubuntu or Debian, or a
Raspberry Pi. See [Before you start](before-you-start.md).

## Do I need a domain name?

For an install on the internet, yes: the UI uses HTTPS, and Let's Encrypt only issues certificates for domain names,
not for bare IP addresses. Any domain (or subdomain) you control works, including free dynamic DNS names, as long as
it has an A record pointing at your server. See [Before you start](before-you-start.md).

On a local network you do not need one: you can use the server's local IP address (next question).

## Can I use it only on my local network?

Yes. Local network mode serves the UI over plain HTTP on port 8080, with no certificate and no ports 80 or 443, and
`DOMAIN` can be the server's local IP address. Passkeys are not available there (browsers allow them only over
HTTPS); passwords and authenticator apps work. Do not use this mode on a server the internet can reach. See
[Local network only](install-local.md).

## Can it be reached by more than one domain?

The UI has exactly one address: `https://` and your `DOMAIN`. Sign-in and every change are accepted only from that
address, so opening the UI by another name does not work. What you can do is give stream clients a different name:
`STREAM_HOST` in `.env` sets the host name encoders and players are told to connect to, if it should not be
`DOMAIN`.

## Can I change the domain later?

Yes. Point the new domain at the server (A record), change `DOMAIN` in `.env`, and run `docker compose up -d`. The
sidecar gets a certificate for the new name by itself. What changes:

- The UI is at the new address only; the old one stops working for sign-in.
- Stream addresses use the new name (unless you set `STREAM_HOST`), so update your encoders. Watch links change
  too: send the new ones around.
- **Passkeys stop working**, because a passkey belongs to the domain it was created on. Sign in with your password
  (and authenticator code), remove the old passkey on your **Account** page and add a new one. Make sure every
  admin knows their password before you switch.

Stream keys, people and settings stay as they are.

## Does it work with IPv6?

Not for publishing: the ports are published on the server's IPv4 addresses only, so your domain needs an A record.
An AAAA record alone does not work, and an AAAA record next to the A record sends IPv6 clients to a closed port
first, so leave it out. The server itself may of course have IPv6 for other things.

## Can I put it behind Cloudflare's proxy?

Use **DNS only** (the grey cloud) for your MediaMTX UI domain. Here is why.

Cloudflare's proxy (the orange cloud) only carries web traffic: HTTP and HTTPS. Streams use other protocols and
ports: RTMP on 1935, RTSP on 8554, SRT on 8890 (UDP) and WebRTC video on 8189 (UDP). With the orange cloud, the
domain resolves to Cloudflare's servers instead of yours, and Cloudflare does not pass those ports on, so encoders
cannot connect and WebRTC video cannot reach viewers. The same name is also what the sidecar tells encoders,
players and browsers to connect to for streams.

If you want Cloudflare's proxy in front of the UI anyway, the stream traffic needs a second name that is DNS only:
create it (for example `media.example.com`, grey cloud, A record to your server) and set `STREAM_HOST` to it in
`.env`. Running the UI behind Cloudflare's proxy is not a tested setup, though: among other things, the sidecar then
sees Cloudflare's addresses instead of your visitors', which its sign-in limits count by. The simple, tested choice
is the grey cloud.

## Where is my data?

In Docker volumes on the server, managed by Docker. Their names start with `mediamtx-ui_`:

| Volume | Holds |
|---|---|
| `data-state` | The database (people, streams, keys, history, audit log), secrets, the HTTPS certificate |
| `data-config` | MediaMTX's configuration, `mediamtx.yml` |
| `data-recordings` | Recordings (or the directory in `RECORDINGS_PATH`) |
| `data-backups` | Encrypted backups (or the directory in `BACKUPS_PATH`) |
| `data-holding` | Holding screen clips |
| `data-logs` | MediaMTX's log |
| `data-hooks` | Hook scripts |

Nothing is sent anywhere else. See [Backups](backups.md) for keeping a copy.

## How do I reset everything and start over?

> [!WARNING]
> This deletes everything: people, streams, keys, settings, the certificate, and the recordings and backups too
> unless you keep them in `RECORDINGS_PATH` and `BACKUPS_PATH`. Download a backup first if you might want anything
> back.

In the directory with `compose.yaml`, stop the stack and delete its volumes:

```bash
docker compose down -v
```

Then start it again; it starts like a fresh install, with a new setup token:

```bash
docker compose up -d
```

The certificate is requested again, which counts against Let's Encrypt's limits, so do not do this repeatedly. See
also [Uninstall](uninstall.md).

## Does it transcode?

No. Streams are passed through as the encoder sends them: same codec, resolution and bitrate. That keeps the server
light (a Raspberry Pi can do it), but it means there is no automatic lower-quality version for slow connections.
Choose encoder settings your viewers can handle. The one place something is converted is a holding screen clip you
upload, and that happens in your browser, not on the server. See [Encoders](encoders.md).

## How many viewers can watch?

MediaMTX UI sets no limit. What limits you is the server's upload bandwidth: every viewer receives the full stream.
A 4 Mbit/s stream watched by 50 people needs about 200 Mbit/s of upload, plus a margin. CPU use is low, because
nothing is transcoded. If you expect a large audience, forward the stream to a platform built for that as well (see
[Forwarding](forwarding.md)).

## How much delay is there?

It depends on how you publish and watch. With WHIP from the encoder and the UI's player or a watch link (WebRTC), the
delay is under a second. RTMP and RTSP add a second or two. HLS, which the player falls back to when WebRTC does not
work, adds several seconds. A 1 second keyframe interval and no B-frames keep it low. See [Watching](watching.md).

## Which OBS settings should I use?

The stream's page shows the address and key, and has presets and step-by-step notes for OBS. In short: a 1 second
keyframe interval, no B-frames (otherwise browsers fall back to HLS), and CBR. For the lowest delay, use OBS 30 or
later with the **WHIP** service. See [Encoders](encoders.md) and [First steps](first-steps.md).

## Can I stream to YouTube or Twitch at the same time?

Yes. Forwarding sends a stream on to YouTube, Twitch or other servers while it is live: you stream once to your
server, and it passes the stream on. See [Forwarding](forwarding.md).

## Can I import my existing MediaMTX configuration?

There is no import button, but you can paste settings into **Configuration** → **YAML**, where admins edit the
whole `mediamtx.yml`. Every save is checked first, and some rules always apply, so a config from a plain MediaMTX
install usually needs changes:

- Authentication stays with MediaMTX UI (`authMethod: http`, pointing at the sidecar). Users and passwords from
  your old file (`authInternalUsers`) do not carry over: create streams and keys in the UI instead.
- The control API stays on and `pprof` stays off; MediaMTX's internal servers cannot be put on a stream port.
- A `recordPath` must be under `/recordings/` and contain `%path`.
- Hooks (`runOn…` settings) cannot be added or changed in the UI yet.

When a save is refused, the message lists every problem. Paths and settings from your old file that pass these rules
work as before. See [Configuration](configuration.md).
