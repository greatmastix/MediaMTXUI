# Local network only

Run MediaMTX UI inside one network (a home, church, school or studio) where nothing needs to be reachable from the
internet. You need no domain, no certificate and no port forwarding: the UI is at `http://<server address>:8080`.

You need a Linux machine with Docker ([Install Docker](install-docker.md)) and a terminal on it. For a server that
people reach from anywhere, use the [public install](install.md) instead.

> [!TIP]
> **The quick way:** the [install script](install-script.md) does all of this (and installs Docker): run
> `curl -fsSL https://github.com/greatmastix/MediaMTXUI/releases/latest/download/install.sh | sudo bash` and choose
> **2, on this local network only**.

## Install

### 1. Create a folder

```bash
mkdir ~/mediamtx-ui && cd ~/mediamtx-ui
```

Run every command on this page from this folder.

### 2. Download the three files

The main file, the settings file, and the add-on for local networks:

```bash
curl -fsSLO https://github.com/greatmastix/MediaMTXUI/releases/latest/download/compose.yaml
```

```bash
curl -fsSL -o .env https://github.com/greatmastix/MediaMTXUI/releases/latest/download/env.example
```

```bash
curl -fsSLO https://github.com/greatmastix/MediaMTXUI/releases/latest/download/compose.lan.yaml
```

Check that all three are there:

```bash
ls -a
```

```
.  ..  .env  compose.lan.yaml  compose.yaml
```

If one is missing, run its command again and look at what it prints. (`curl: command not found`: install it with
`sudo apt install curl`.)

### 3. Find the server's address

```bash
hostname -I
```

The first address it prints is usually the right one, for example `192.168.1.50`. Ignore `172.17.0.1` and other
`172.x` addresses Docker adds.

### 4. Set two lines in .env

```bash
nano .env
```

Change the `DOMAIN` line at the top to the address from step 3, and add the `COMPOSE_FILE` line at the end:

```
DOMAIN=192.168.1.50
COMPOSE_FILE=compose.yaml:compose.lan.yaml
```

`COMPOSE_FILE` makes every `docker compose` command use the main file together with the local add-on. Leave
everything else as it is. Save with **Ctrl+O** and **Enter**, leave with **Ctrl+X**.

### 5. Start it

```bash
docker compose up -d
```

The first start downloads the images, which takes a minute or two. Check that both containers are up (the sidecar
says `healthy`):

```bash
docker compose ps
```

### 6. Open it and create the admin

Get the setup token:

```bash
docker compose exec sidecar /mtxui setup-token
```

On any device in the network, open `http://192.168.1.50:8080` (your address), enter the token, and create the first
admin. The setup wizard is explained in [Install](install.md#8-the-setup-wizard); continue with
[First steps](first-steps.md).

## Good to know

- **Always use exactly that address.** Open the UI at the address in `DOMAIN` with `:8080`. Another name or IP for the
  same machine shows the page, but sign-in fails with "Cross-origin request refused.". To change the address, edit
  `DOMAIN` and run `docker compose up -d` again.
- **Keep the address from changing.** Your router may give the server a new address after a restart. In the router's
  settings, reserve the current one for it (called **DHCP reservation**, "static lease" or "address reservation").
  A local name such as `mediamtx.local` works too, if every device resolves it.
- **Streams** use the same ports as everywhere: 1935/tcp RTMP, 8554/tcp RTSP, 8890/udp SRT, 8189/udp WebRTC. Encoders
  and players connect to the same address; each stream's page shows the exact URLs.
- **No passkeys.** Browsers allow them only over HTTPS, so the UI does not offer them. Passwords and authenticator apps
  work.
- **Another UI port:** add `UI_PORT=8090` to `.env` and open `http://192.168.1.50:8090`.
- **Keep the internet out** by not forwarding any port to the server in your router. A firewall on the server itself
  (`ufw`) does not limit Docker's published ports, because Docker's rules come first. On a machine with several
  networks, `UI_BIND=192.168.1.50` and `STREAM_BIND=192.168.1.50` in `.env` publish the ports on the LAN address only.
- **Do not use this on a server the internet can reach:** sign-in would travel unencrypted.

Upgrading works as for every install, see [Upgrading](upgrading.md): download the new `compose.yaml` and
`compose.lan.yaml`, then `docker compose pull` and `docker compose up -d`.

## Try it on your own computer

To try MediaMTX UI on Windows, macOS or a Linux desktop, install Docker Desktop and follow the steps above with two
differences:

- In step 4, set `DOMAIN=localhost`. On Windows, outside WSL, separate the files with a semicolon:
  `COMPOSE_FILE=compose.yaml;compose.lan.yaml`. (Edit `.env` with any text editor, for example `notepad .env`.)
- Open `http://localhost:8080`, not `http://127.0.0.1:8080`.

Only this computer can then use the UI and its streams; OBS on it streams to `rtmp://localhost/...`. Browsers treat
`localhost` as secure, so passkeys work here. To remove it again, see [Uninstall](uninstall.md).

## HTTPS on a local network (advanced)

For HTTPS (and passkeys) inside the network, you need a certificate your devices trust, for a name that resolves to
the server:

- **Your own reverse proxy** with an internal certificate (Caddy's internal CA, or your own CA): use the reverse-proxy
  add-on instead of the local one, see [Behind your own reverse proxy](behind-a-proxy.md).
- **A private ACME server** such as step-ca: use the public install without the local add-on, set `ACME_DIRECTORY` to
  your server's directory URL, and point `MTXUI_ACME_CA_CERT` at your CA's certificate through a
  `compose.override.yaml` ([All settings](config.md)). `DOMAIN` must be a name with a dot, not an IP address.

Either way, every device must trust your CA's certificate.
