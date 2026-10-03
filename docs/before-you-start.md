# Before you start

This page helps you plan an install before you type any command: which machine to run MediaMTX UI on, whether it
should be reachable from the internet or only on your own network, the domain name and DNS record you need, and which
ports to open. It ends with a checklist. If you already run servers, skim the tables and the checklist.

## The short version

- A Linux machine with Docker and the Compose plugin ([Install Docker](install-docker.md)): a small cloud server, a
  home server or a Raspberry Pi 4 or 5.
- For an install people reach from the internet: a domain name with an **A record** (IPv4) pointing at the machine,
  and ports **80** and **443** (TCP) open, plus the stream ports you use.
- For a home or studio network only: no domain and no open ports. See [Local network only](install-local.md).

## Where to run it

MediaMTX UI is two containers: MediaMTX, which carries the video, and the "sidecar", which serves the web UI and
tells MediaMTX who may stream and watch. They run anywhere Docker runs on Linux: the images are built for amd64
(ordinary PCs and most cloud servers), arm64 (Raspberry Pi 4 and 5 with a 64-bit system, ARM cloud servers) and armv7
(32-bit Raspberry Pi OS).

| Option | Good for | Watch out for |
|---|---|---|
| **A cloud server (VPS)** from any provider | Streams people watch from anywhere; a fixed public IPv4 address; fast upload | Monthly cost; traffic allowances (video adds up, see below) |
| **A home or office server** (an old PC, a mini PC, a NAS that runs Docker) | Streaming inside a building; cameras on the same network; no monthly cost | Your internet upload speed limits outside viewers; your router must forward ports; some connections cannot accept incoming traffic at all (see [CGNAT](#cgnat-and-ipv6-only-connections)) |
| **A Raspberry Pi 4 or 5** | A small, quiet, always-on box for a church, school or home | Use the 64-bit system; keep recordings on a USB SSD rather than the SD card |

### CPU and memory

MediaMTX does not transcode: it passes on the video exactly as your encoder sends it, and only repackages it for each
protocol. That takes little CPU, so even a Raspberry Pi or the smallest cloud server with one or two cores copes with
several streams. (The one heavy job, converting a holding-screen clip, happens in your browser, not on the server.)

The compose file caps the containers' memory: **512 MB** for the sidecar and **2 GB** for MediaMTX. These are upper
limits, not amounts set aside, but they tell you what the stack may use at most. A machine with **2 GB of RAM or
more** leaves room for both and for the system itself.

### Network: the part that usually matters most

Because nothing is transcoded, every viewer receives the full bitrate of the stream. What the server sends out is
roughly:

```
upload needed ≈ stream bitrate × number of viewers
```

A 6 Mbit/s stream watched by 20 people needs about 120 Mbit/s of upload. At home, your connection's upload speed is
usually the limit; on a cloud server, check the provider's bandwidth and monthly traffic allowance.

### Disk space for recordings

Recordings are only made for streams where you switch **Record this stream** on. When you do, they take space at the
rate the encoder sends:

```
GB per hour ≈ bitrate in Mbit/s × 0.45
```

| Bitrate | Per hour | Per day of continuous recording |
|---|---|---|
| 2.5 Mbit/s (720p) | about 1.1 GB | about 27 GB |
| 6 Mbit/s (1080p) | about 2.7 GB | about 65 GB |
| 10 Mbit/s | about 4.5 GB | about 108 GB |
| 20 Mbit/s | about 9 GB | about 216 GB |

Out of the box, MediaMTX deletes recordings older than 7 days, and the sidecar protects the disk: it deletes the
oldest recordings while the disk has less than 20 GB free, and switches recording off below 5 GB free. On a small disk
(a Raspberry Pi's SD card, a 25 GB cloud server) lower those limits, or keep recordings on a bigger disk; see
[Recordings](recordings.md). Without recordings, the install itself needs only a few GB for the system, Docker and
the two images.

## Public or local only

Decide who needs to reach the server:

- **Public** (the usual install, [Install](install.md)): the UI is at `https://your-domain`, with a free certificate
  from Let's Encrypt that the sidecar gets and renews by itself. Encoders and viewers can connect from anywhere. You
  need a domain name and open ports.
- **Local network only** ([Local network only](install-local.md)): the UI is at `http://192.168.x.x:8080` (or a local
  name), over plain HTTP, reachable only inside your network. No domain, no certificate, no port forwarding. Passkeys
  are not available there (they need HTTPS); passwords and authenticator apps work.

If you already run a web server such as Caddy, nginx or Traefik on the machine, it can serve the UI's HTTPS instead:
see [Behind your own reverse proxy](behind-a-proxy.md).

## A domain name and an A record

A public install needs a name such as `stream.example.com`, because Let's Encrypt only issues certificates for names,
not for bare IP addresses.

**DNS** is the internet's phone book: it turns a name into the address of a machine. An **A record** is one entry in
it, saying "this name belongs to this IPv4 address".

1. **Get a domain**, if you do not have one, from any domain registrar. You can use a subdomain of a domain you
   already own (`stream.yourchurch.org`); it does not have to be a new domain.
2. **Find your server's public IPv4 address.** A cloud provider shows it in its control panel. At home, it is your
   router's public address: run this on the server, or search "what is my IP" in a browser on the same network:

   ```bash
   curl -4 https://ifconfig.me
   ```

3. **Create the A record** in your DNS provider's control panel (often the registrar):

   | Type | Name | Value |
   |---|---|---|
   | A | `stream` (for `stream.example.com`) | your server's IPv4 address, e.g. `203.0.113.10` |

   Some panels want the full name, some only the part before your domain. The "TTL" can stay at its default.
4. **Check it.** DNS changes often work within minutes but can take longer. On Linux or macOS:

   ```bash
   dig +short A stream.example.com
   ```

   On Windows, or if `dig` is missing:

   ```bash
   nslookup stream.example.com
   ```

   When it works, the answer is your server's IPv4 address. If you get nothing or an old address, wait a while and try
   again.

> [!NOTE]
> MediaMTX UI publishes its ports on IPv4 only. Do not create an AAAA (IPv6) record for this name: on its own it does
> not work, and next to an A record it sends IPv6 visitors to a closed port first, which makes the page slow or fail
> to load for them.

A home connection's public address may change from time to time. If yours does, use your DNS provider's or router's
"dynamic DNS" feature to keep the A record up to date.

## Ports

A **port** is a numbered door on the server; each kind of traffic uses its own. Open the ones you need in every
firewall between the internet and the server.

| Port | Protocol | What uses it | Open it when |
|---|---|---|---|
| 80 | TCP | Let's Encrypt's check of your domain, and a redirect to HTTPS | Always, for a public install |
| 443 | TCP | The web UI (HTTPS), watch links, and browsers' live view | Always, for a public install |
| 1935 | TCP | RTMP: OBS and most encoders | You stream with RTMP (the usual choice) |
| 8554 | TCP | RTSP: IP cameras, VLC, ffmpeg, VRChat | You use RTSP |
| 8890 | UDP | SRT: streaming over unstable or long-distance links | You use SRT |
| 8189 | UDP | WebRTC media: watching in the browser with low delay, and WHIP encoders | You watch in the browser (recommended) |

Only open the stream ports you use. MediaMTX's own API, metrics and playback ports are never published, so you do not
need to think about them. Without UDP 8189, browsers still play the stream, but over HLS, a few seconds behind.

For a local-network install, the UI uses port **8080** (TCP) instead of 80 and 443, and nothing needs to be opened on
your router.

### Cloud firewalls (security groups)

Most cloud providers put a firewall in front of every server, called a firewall, security group or network ACL. New
servers often allow only SSH (port 22). Add an inbound rule for each port in the table above, with source "anywhere"
(`0.0.0.0/0`).

> [!WARNING]
> A firewall *on* the server, such as `ufw` or `firewalld`, does not protect Docker's published ports: Docker adds its
> own rules ahead of them, so a published port is reachable whatever `ufw` says. Use the cloud provider's firewall, or
> your router, to decide what is reachable. (Keep SSH allowed in whatever you change.)

### Home router port forwarding

At home, your router hides the machines behind it. To make the server reachable from the internet, forward each port
from the router to the server:

1. Give the server a fixed address on your network: in the router's settings, find "DHCP reservation" (or "static
   lease", "always assign this IP") and reserve the server's current address for it.
2. Find "port forwarding" (or "virtual server", "NAT rules") in the router's settings.
3. Add one rule per port from the table: external port, the same internal port, the protocol (TCP or UDP), and the
   server's address as the destination.

Router menus differ by brand; the router's manual or the provider's help pages show where these settings are.

### CGNAT and IPv6-only connections

Some internet connections cannot accept incoming connections on IPv4 at all, whatever you set on your router:

- **CGNAT** (carrier-grade NAT): your provider shares one public IPv4 address between many customers. A sign: the
  WAN address your router shows starts with `100.64.` to `100.127.`, or with `10.`, `172.16.` to `172.31.`, or
  `192.168.`, and differs from what `curl -4 https://ifconfig.me` reports.
- **IPv6-only** connections, or "DS-Lite", where IPv4 goes through the provider's shared gateway.

Since MediaMTX UI publishes on IPv4 only, a public install does not work behind these. Your options: ask your
provider for a public IPv4 address (some offer it on request or for a small fee), run the server at a cloud provider
instead, or use a [local-network install](install-local.md) for streaming inside the building.

## Checklist

Before you go on to [Install](install.md):

- [ ] A Linux machine with at least 2 GB of RAM, enough disk for the recordings you plan, and enough upload speed for
      your viewers.
- [ ] You can open a terminal on it, or connect with SSH ([Install Docker](install-docker.md#open-a-terminal-on-the-server)).
- [ ] Docker and the Compose plugin are installed: `docker compose version` answers ([Install Docker](install-docker.md)).
- [ ] Public install: a domain name with an A record pointing at the server's public IPv4 address, checked with
      `dig` or `nslookup`, and no AAAA record for it.
- [ ] Public install: ports 80 and 443 (TCP) open in the cloud firewall or forwarded by your router, and the stream
      ports you use (1935/tcp, 8554/tcp, 8890/udp, 8189/udp).
- [ ] Public install: no other web server already uses ports 80 and 443 on the machine (if one does, see
      [Behind your own reverse proxy](behind-a-proxy.md)).
- [ ] Local only: the server has a fixed address on your network ([Local network only](install-local.md)).
