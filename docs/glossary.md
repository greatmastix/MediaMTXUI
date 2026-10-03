# Glossary

Short explanations of the words these pages use, in plain language. If a page uses a word you do not know, look it
up here. The words are grouped by topic.

## Servers and Docker

**Docker**
: A program that runs software in sealed-off packages called containers, so that it runs the same on any Linux
  machine and does not mix with the rest of the system. MediaMTX UI is installed with Docker. See
  [Install Docker](install-docker.md).

**Image**
: The packaged program that a container is started from, downloaded from the internet: for example
  `ghcr.io/greatmastix/mediamtxui:1.2.3`. An image never changes; a new version is a new image.

**Container**
: A running copy of an image. MediaMTX UI runs two containers: `mediamtx` (the streaming server) and `sidecar` (the
  web UI). Removing a container does not remove your data, which lives in volumes.

**Volume**
: A storage area Docker keeps for a container, which survives when the container is replaced (for example during an
  upgrade). MediaMTX UI's data is in volumes whose names start with `mediamtx-ui_`. See
  [FAQ: Where is my data?](faq.md#where-is-my-data).

**Docker Compose**
: The part of Docker that starts several containers together from one description file, with commands like
  `docker compose up -d` (start), `docker compose ps` (show) and `docker compose logs` (show logs). Run them in the
  directory with `compose.yaml`.

**compose.yaml**
: The file that tells Docker Compose which containers to run, from which images, with which ports and volumes. You
  download it from a release and do not edit it; an upgrade replaces it. See [Upgrading](upgrading.md).

**Override (compose.override.yaml)**
: An extra Compose file next to `compose.yaml` that adds or changes settings. Your own settings go into
  `compose.override.yaml`; optional setups (a reverse proxy, local network only, exposure control) come as override
  files of their own.

**.env**
: A small text file next to `compose.yaml` with your settings, such as `DOMAIN=stream.example.com`. Docker Compose
  reads it by itself. The leading dot makes it a hidden file: `ls -a` shows it.

## Networks

**IP address**
: The number that identifies a computer on a network, such as `203.0.113.7` (IPv4). A server on the internet has a
  public IP address; a computer at home has a local one, such as `192.168.1.50`.

**Port**
: A numbered door on a computer that a particular service listens at. A web server listens at 443 (HTTPS), RTMP at
  1935. "Opening a port" means letting connections to it through the firewall. Ports come as TCP or UDP, two ways of
  sending data; a firewall rule is for one of them.

**Firewall**
: A filter that decides which connections may reach a computer. Cloud providers have one in front of every server
  (often called a security group); routers have one at home. You open the ports MediaMTX UI needs there.

**Port forwarding**
: A router setting that passes connections arriving at your home's public address on a port to one computer inside
  your network. Needed when the server is at home, such as a Raspberry Pi.

**Domain**
: A name for a server, such as `stream.example.com`, that is easier to remember than an IP address. You rent one from
  a domain registrar, or use a subdomain of one you already have.

**DNS**
: The internet's address book, which turns a domain into an IP address.

**A record**
: The DNS entry that says which IPv4 address a domain points to. MediaMTX UI needs one for its domain. (An AAAA record
  is the same for IPv6, which MediaMTX UI does not publish on.)

**TLS / HTTPS**
: Encryption for web traffic. HTTPS is a web address (`https://`) protected by TLS, so nobody in between can read
  passwords or change pages. It needs a certificate for the domain.

**Let's Encrypt**
: A free service that issues the certificates HTTPS needs. MediaMTX UI gets one from Let's Encrypt by itself and
  renews it before it expires.

**Reverse proxy**
: A web server (such as Caddy, nginx or Traefik) that receives all HTTPS traffic for a server and passes it on to the
  right program. If you already run one, MediaMTX UI can sit behind it. See
  [Behind a reverse proxy](behind-a-proxy.md).

## MediaMTX UI

**MediaMTX**
: The open-source streaming server that does the actual work: it receives streams from encoders and passes them on
  to viewers, and records them. MediaMTX UI runs the official MediaMTX image, unchanged, and is not part of the
  MediaMTX project.

**Sidecar**
: MediaMTX UI's own container, which runs next to MediaMTX: the web UI, accounts, keys, HTTPS, backups. It writes
  MediaMTX's configuration and decides who may stream and watch.

**Path**
: MediaMTX's name for a place a stream is published to and read from, such as `live` or `church/main`. Every stream
  has a path; the **Paths** page shows them all.

**Stream**
: In MediaMTX UI, a path with its own page, keys, owner and settings, created on the **Streams** page. See
  [Streams](streams.md).

**Holding screen**
: A clip that plays on a stream while nobody streams to it, such as an "offline" image. See
  [Holding screens](holding-screens.md).

**Step-up ("Confirm it's you")**
: A second check of who you are before an admin-level action, such as changing people, keys or backups, even though
  you are already signed in. You answer with your password, an authenticator code or a passkey.

## Streaming

**Encoder**
: The program or device that sends video to the server: OBS Studio, ffmpeg, a hardware encoder, an IP camera, a phone
  app. See [Encoders](encoders.md).

**RTMP**
: The protocol most streaming software uses to send video, such as OBS's default. Port 1935/tcp. Not encrypted.

**RTSP**
: A protocol used by IP cameras and video players, to send or receive video. Port 8554/tcp.

**SRT**
: A protocol for sending video over unreliable networks (mobile, long distance), which recovers lost data. Port
  8890/udp. It can be encrypted with a passphrase.

**WebRTC**
: The technology browsers use for real-time video. MediaMTX UI's player uses it to show streams with less than a
  second of delay. Its video travels over port 8189/udp.

**WHIP**
: A way to *send* a stream over WebRTC, supported by OBS 30 and later. It gives the lowest delay.

**WHEP**
: A way to *watch* a stream over WebRTC, for players that support it.

**HLS**
: A way to watch video in small downloaded pieces over ordinary HTTPS. It works almost everywhere, but plays several
  seconds behind. The player falls back to it when WebRTC does not work.

**Bitrate**
: How much data a stream uses per second, for example 4 Mbit/s (4 megabits per second). Higher means better quality,
  but needs more upload speed from the encoder and from the server for every viewer.

**Keyframe**
: A complete picture in a video stream; the frames in between only store changes. A player can only start at a
  keyframe, so a keyframe every second (a "keyframe interval" of 1 s) lets viewers start quickly.

**B-frames**
: Frames that refer to pictures both before and after them, which saves some bitrate. Browsers cannot play H.264 video
  with B-frames over WebRTC, so set B-frames to 0 in your encoder.

**Transcoding**
: Converting video into another format, size or bitrate while it streams. MediaMTX UI does not transcode: streams
  pass through as the encoder sends them.

## Sign-in

**Passkey**
: A way to sign in without typing a password, using your phone, computer or a security key (fingerprint, face or PIN).
  A passkey belongs to one domain and works only over HTTPS.

**TOTP (authenticator app)**
: A six-digit code that changes every 30 seconds, shown by an app on your phone (such as Google Authenticator,
  Aegis or 1Password). Asked for after your password, it is a second factor: someone who has your password still
  cannot sign in without your phone. Recovery codes stand in for it if you lose the phone.

**Join code**
: A one-time code that lets someone set their password: for a new person you invite, or after a password reset. See
  [People and access](people.md).
