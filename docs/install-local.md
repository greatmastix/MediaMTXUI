# Local network only

This page is for running MediaMTX UI inside one network, such as a home, a church, a school or a studio, where nothing
needs to be reachable from the internet. The UI is served over plain HTTP on port 8080 at the server's local address,
with no domain, no certificate and no port forwarding. It also covers trying MediaMTX UI on your own computer with
Docker Desktop. For an install people reach from anywhere, see [Install](install.md).

## The short version

In the install folder, next to `compose.yaml` and `.env` (steps 1 and 2 of [Install](install.md)):

```bash
curl -fsSLO https://github.com/greatmastix/MediaMTXUI/releases/latest/download/compose.lan.yaml
```

In `.env`, set `DOMAIN` to the server's LAN address and add the `COMPOSE_FILE` line:

```
DOMAIN=192.168.1.50
COMPOSE_FILE=compose.yaml:compose.lan.yaml
```

```bash
docker compose up -d
```

Open `http://192.168.1.50:8080` and finish the setup with the token from
`docker compose exec sidecar /mtxui setup-token`.

## When to choose it

Choose local mode when:

- encoders and viewers are all on the same network (cameras in a hall, OBS on a PC next to the server, screens in other
  rooms);
- you have no domain name, or your internet connection cannot accept incoming connections
  ([CGNAT](before-you-start.md#cgnat-and-ipv6-only-connections));
- you want to try MediaMTX UI before setting up a public server.

Do not use it on a server the internet can reach: passwords would travel unencrypted. If people outside your network
should watch, use the [public install](install.md).

## What is different

The local override, `compose.lan.yaml`, changes three things in the sidecar:

- The UI is plain **HTTP**, at `http://DOMAIN:8080`, instead of HTTPS.
- There is **no certificate** and no Let's Encrypt; ports 80 and 443 are not used at all.
- The UI port is **8080** on the server (set `UI_PORT` in `.env` to use another).

The stream ports are the same as in the public install (1935/tcp RTMP, 8554/tcp RTSP, 8890/udp SRT, 8189/udp WebRTC),
and encoders and players connect to `DOMAIN` on them.

What does not work without HTTPS:

- **Passkeys.** Browsers allow them only on HTTPS pages (or `localhost`), so the UI does not offer them. Passwords and
  authenticator apps (second factor) work as usual.
- **Let's Encrypt**, which only issues certificates for public domain names. For HTTPS on a LAN, see
  [HTTPS on a local network](#https-on-a-local-network-advanced).

## 1. Find the server's LAN address

The LAN address is the server's address inside your network, usually starting with `192.168.`, `10.` or `172.16.` to
`172.31.`. On the server:

```bash
hostname -I
```

prints its addresses, the first one usually being the one you want, for example `192.168.1.50`. For more detail
(which network card has which address):

```bash
ip -4 addr
```

Look for the `inet` line under your Ethernet (`eth0`, `enp...`) or Wi-Fi (`wlan0`, `wlp...`) interface, and ignore
`127.0.0.1` (the machine itself) and `172.17.0.1` or similar on `docker0` (Docker's own network).

## 2. Give the server a fixed address

Your router hands out addresses automatically (DHCP), and a machine can get a different one after a restart. Since
the UI is only reachable at the exact address you configure, make sure it stays the same: in your router's settings,
find **DHCP reservation** (also called "static lease", "address reservation" or "always use this IP address") and
reserve the server's current address for it. Router menus differ; your router's manual shows where it is.

Instead of an IP address, you can use a name your network resolves to the server, such as `mediamtx.local` or
`mediamtx.home.arpa`, if your router or local DNS server provides one. Every device that opens the UI or connects an
encoder must resolve that name.

## 3. Download the override and set .env

Do steps 1 and 2 of [Install](install.md) (create the folder, download `compose.yaml` and `.env`), then download the
local override into the same folder:

```bash
curl -fsSLO https://github.com/greatmastix/MediaMTXUI/releases/latest/download/compose.lan.yaml
```

Open `.env`:

```bash
nano .env
```

and set:

```
DOMAIN=192.168.1.50
COMPOSE_FILE=compose.yaml:compose.lan.yaml
```

- **DOMAIN**: the server's LAN address from step 1 (or the local name), without `http://` and without the port.
- **COMPOSE_FILE**: tells every `docker compose` command in this folder to use both files, the main one and the local
  override. Keep this line: upgrades and other commands rely on it.
- **ACME_EMAIL** is not used in local mode; leave it empty.

Save with **Ctrl+O**, **Enter**, and leave with **Ctrl+X**.

## 4. Start it and finish the setup

```bash
docker compose up -d
```

Check that both containers run (the sidecar `Up ... (healthy)`, MediaMTX `Up ...`):

```bash
docker compose ps
```

Get the setup token:

```bash
docker compose exec sidecar /mtxui setup-token
```

Open `http://192.168.1.50:8080` (your address) in a browser on any device in the network, and complete the setup
wizard as described in [Install](install.md#8-the-setup-wizard). Then continue with [First steps](first-steps.md).

## Use exactly that address

Open the UI only by exactly the address in `DOMAIN`, with `:8080`. If `DOMAIN` is `192.168.1.50`, then
`http://mediamtx.local:8080`, `http://localhost:8080` or another address of the same machine show the page, but
signing in fails with "Cross-origin request refused.": the sidecar accepts sign-ins and changes only from pages
opened at its configured address. If you want to
use a name later, change `DOMAIN` and run `docker compose up -d` again.

## Firewall on the server

Your router is what keeps the internet out: without port forwarding, nothing outside your network reaches the
server. Do not forward any port to it in local mode.

A firewall on the server itself, such as `ufw`, is not a reliable way to limit who reaches the published ports:
Docker inserts its own rules ahead of `ufw`'s, so ports published by Docker are reachable whatever `ufw` allows or
denies (see [Exposure control](exposure-control.md#what-it-needs)). If the server has more than one network
connection (a second network card, a VPN), you can publish the ports only on the LAN address instead, in `.env`:

```
UI_BIND=192.168.1.50
STREAM_BIND=192.168.1.50
```

and run `docker compose up -d` again. The UI and stream ports then answer only on that address.

## Try it on your own computer

To try MediaMTX UI on your Windows, Mac or Linux computer before setting up a server, install Docker Desktop (or
Docker on Linux, see [Install Docker](install-docker.md)) and use local mode with `localhost`, the name every computer
has for itself.

1. Create a folder and download the three files into it (in PowerShell, Terminal or a WSL terminal):

   ```bash
   curl -fsSLO https://github.com/greatmastix/MediaMTXUI/releases/latest/download/compose.yaml
   ```

   ```bash
   curl -fsSL -o .env https://github.com/greatmastix/MediaMTXUI/releases/latest/download/env.example
   ```

   ```bash
   curl -fsSLO https://github.com/greatmastix/MediaMTXUI/releases/latest/download/compose.lan.yaml
   ```

2. In `.env` (open it with any text editor, for example `notepad .env` on Windows), set:

   ```
   DOMAIN=localhost
   COMPOSE_FILE=compose.yaml:compose.lan.yaml
   ```

   On Windows, outside WSL, separate the two files with a semicolon instead:
   `COMPOSE_FILE=compose.yaml;compose.lan.yaml`.
3. Start it and get the setup token:

   ```bash
   docker compose up -d
   ```

   ```bash
   docker compose exec sidecar /mtxui setup-token
   ```

4. Open `http://localhost:8080`. (Not `http://127.0.0.1:8080`: see [Use exactly that address](#use-exactly-that-address).)

With `localhost`, only this computer can use the UI and the streams; OBS on the same computer streams to
`rtmp://localhost/...` as the stream page shows. Browsers treat `localhost` as a secure place, so passkeys are offered
here even without HTTPS. To remove the trial again, see [Uninstall](uninstall.md).

## HTTPS on a local network (advanced)

If you want HTTPS (and passkeys) inside your network, you need a certificate your devices trust for a name that
resolves to the server. Two ways:

- **Your own reverse proxy** with an internal certificate (for example Caddy's internal CA, or a certificate from your
  own CA), serving the UI at `https://name`: use the reverse-proxy override instead of the local one, as described in
  [Behind your own reverse proxy](behind-a-proxy.md). `DOMAIN` is then the name the proxy serves.
- **A private ACME server** (such as step-ca) instead of Let's Encrypt, with the public install's `compose.yaml` and no
  local override: set `ACME_DIRECTORY` in `.env` to your server's directory URL, and point `MTXUI_ACME_CA_CERT` at the
  PEM file of your CA, mounted into the sidecar through a `compose.override.yaml`. `DOMAIN` must be a name with a dot
  (not an IP address or a single word). See [Configuration reference](config.md).

Either way, every device that opens the UI must trust your CA's certificate.
