# Install

This page walks you through the public install: MediaMTX UI on a Linux server, reachable at `https://your-domain`
with a certificate from Let's Encrypt, step by step, with what each step does and what you should see. It assumes
Docker is installed ([Install Docker](install-docker.md)), and that your domain points at the server and ports 80 and
443 are open ([Before you start](before-you-start.md)). For a home or studio network without a domain, see
[Local network only](install-local.md) instead.

## The short version

```bash
mkdir mediamtx-ui && cd mediamtx-ui
```

```bash
curl -fsSLO https://github.com/greatmastix/MediaMTXUI/releases/latest/download/compose.yaml
```

```bash
curl -fsSL -o .env https://github.com/greatmastix/MediaMTXUI/releases/latest/download/env.example
```

Set `DOMAIN` (and `ACME_EMAIL`) in `.env`, then:

```bash
docker compose up -d
```

```bash
docker compose exec sidecar /mtxui setup-token
```

Open `https://your-domain`, enter the token, create the first admin. The rest of this page explains each step.

## 1. Create a folder

The install lives in one folder: the compose file, your settings, and nothing else (the data is kept by Docker, see
[Where your data lives](#where-your-data-lives)). Create it in your home directory and go into it:

```bash
mkdir mediamtx-ui && cd mediamtx-ui
```

Every `docker compose` command for this install must be run from this folder. When you connect again later, go back
into it first:

```bash
cd ~/mediamtx-ui
```

## 2. Download the two files

Each release of MediaMTX UI comes with a `compose.yaml` that names the exact versions of both images (the sidecar of
that release and the MediaMTX it was tested with), and an example settings file. Download both from the latest
release. The second command saves the example directly as `.env`, the file Docker Compose reads settings from:

```bash
curl -fsSLO https://github.com/greatmastix/MediaMTXUI/releases/latest/download/compose.yaml
```

```bash
curl -fsSL -o .env https://github.com/greatmastix/MediaMTXUI/releases/latest/download/env.example
```

Neither prints anything when it works. Check that both files are there (`-a` also lists files whose name starts with
a dot, like `.env`):

```bash
ls -a
```

```
.  ..  .env  compose.yaml
```

If the shell says `curl: command not found`, install it (`sudo apt install curl` on Ubuntu, Debian and Raspberry Pi
OS, `sudo dnf install curl` on Fedora) and run the two commands again.

> [!NOTE]
> Do not edit `compose.yaml`. An upgrade replaces it with the new release's copy ([Upgrading](upgrading.md)). Your
> settings go into `.env`, and anything `.env` does not cover into a `compose.override.yaml` next to it
> ([Configuration reference](config.md)).

## 3. Edit .env

Open the settings file in `nano`, a simple text editor that runs in the terminal:

```bash
nano .env
```

Move around with the arrow keys and change the two lines at the top:

```
DOMAIN=stream.example.com
ACME_EMAIL=you@example.com
```

- **DOMAIN** (required): the name people type to reach the UI, without `https://` and without a slash at the end. It
  must be the name your A record points at this server. The UI is then at `https://` plus this name, and encoders and
  players connect to the stream ports at the same name.
- **ACME_EMAIL** (optional): where Let's Encrypt may send a warning if your certificate is about to expire. Renewal is
  automatic, so such mails are rare. Leave it empty if you like.

To save, press **Ctrl+O** (the letter O), then **Enter** to confirm the file name. To leave nano, press **Ctrl+X**.
(If nano is missing: `sudo apt install nano`.)

Everything else in `.env` is optional and commented out (a line starting with `#` is ignored). The ones you may want
now:

| Line | What it does |
|---|---|
| `ACME_DIRECTORY=...staging...` | Uses Let's Encrypt's test server for a first try ([see below](#tip-try-with-lets-encrypts-staging-server-first)). |
| `RECORDINGS_PATH=/srv/recordings` | Keeps recordings in a directory of your choice, for example on a bigger disk ([see below](#recordings-and-backups-on-a-disk-of-your-choice)). |
| `BACKUPS_PATH=/srv/mediamtx-backups` | The same for backups. |
| `RECORDINGS_MAX_GB=0` | A storage budget for recordings in GB: beyond it the oldest are deleted. `0` means no budget ([Recordings](recordings.md)). |
| `STREAM_HOST=` | The name encoders and players connect to, if it is not `DOMAIN`. |
| `COMPOSE_FILE=` | Extra compose files: [your own reverse proxy](behind-a-proxy.md), [local network only](install-local.md), [exposure control](exposure-control.md). |
| `HISTORY_DAYS=30` | Days of chart history kept (1 to 365). |
| `LOG_LEVEL=info` | How much the sidecar logs: `debug`, `info`, `warn` or `error`. |

To use one, remove the `#` at its start and set the value. [Configuration reference](config.md) lists every setting
of the sidecar.

No passwords or secrets go into `.env`: the sidecar generates its own on the first start and keeps them in its data.

## 4. Start it

```bash
docker compose up -d
```

Docker Compose downloads the two images (the first time, this takes a minute or two, depending on your connection),
creates the containers and their storage, and starts them in the background (`-d`, "detached"). At the end you see
lines like:

```
 ✔ Container mediamtx-ui-sidecar-1   Healthy
 ✔ Container mediamtx-ui-mediamtx-1  Started
```

MediaMTX starts only once the sidecar reports itself healthy, because the sidecar writes MediaMTX's configuration
first.

If it stops with `required variable DOMAIN is missing a value: set DOMAIN in .env`, the `DOMAIN` line in `.env` is
empty or missing. If it says `port is already allocated` or `address already in use`, another program (often a web
server) uses port 80 or 443: see [Behind your own reverse proxy](behind-a-proxy.md).

## 5. Watch it start

See the state of both containers:

```bash
docker compose ps
```

When all is well, the sidecar's status says `Up ... (healthy)` and MediaMTX's `Up ...`. A status of `Restarting`
means a container keeps stopping: its log says why.

Follow the sidecar's log (press **Ctrl+C** to stop watching; the containers keep running):

```bash
docker compose logs -f sidecar
```

The sidecar logs one JSON line per event. On a first start, the important parts are:

- `"msg":"starting"` with the version, then `"msg":"listening"` lines for its ports.
- A box headed **MediaMTX UI first-run setup** with the setup token (see [step 7](#7-get-the-setup-token)).
- `"msg":"HTTPS with ACME certificates"` with your domain as `host`: the sidecar is ready to get a certificate. It asks
  Let's Encrypt for it right away, which takes a few seconds.

If something is wrong with the certificate, a warning follows within a minute:

```
"level":"WARN","msg":"no certificate yet; check that the domain points here and ports 80 or 443 are reachable"
```

with Let's Encrypt's reason in `err`. Usually the A record does not point at this server yet (check it with `dig`,
see [Before you start](before-you-start.md#a-domain-name-and-an-a-record)), or a firewall blocks ports 80 and 443.
Fix that; the sidecar tries again when the next browser connects, so there is no need to restart it. No warning means
the certificate was issued.

If the sidecar stops right away with `invalid settings` and a list of problems, fix the named settings in `.env` (or
your override) and run `docker compose up -d` again.

## 6. Open the site

In your browser, open `https://` and your domain, for example `https://stream.example.com`. You should get the page
**Set up MediaMTX UI**, with a padlock in the address bar.

Use exactly the address in `DOMAIN`. Other names or the server's IP address do not work: the sidecar only accepts
requests made through the address it was configured with.

## 7. Get the setup token

The setup token proves that the person setting up the server is someone who can run commands on it. Show it with:

```bash
docker compose exec sidecar /mtxui setup-token
```

It prints the token on one line. (It is also in the box in the sidecar's log from step 5.) The token works only until
setup is done; after that, the command says `no setup token: setup is done`.

## 8. The setup wizard

The setup page asks for:

1. **Setup token**: paste the token from step 7. Dashes, spaces and upper or lower case do not matter.
2. **Admin username**: the first admin's name, `admin` unless you change it (2 to 32 letters, digits, dots,
   underscores or hyphens, starting with a letter or digit).
3. **Password** and **Password again**: at least 12 characters. Use a password manager if you can.
4. **Accept streams over**: tick the protocols your encoders will use. All are off unless you tick them; you can
   change this later under **Configuration** → **Quick setup** → **Choose the protocols to serve**.
   - **RTMP**: OBS and streaming software, port 1935/tcp. Tick this if you are unsure.
   - **RTSP**: cameras, OBS, ffmpeg, port 8554/tcp.
   - **SRT**: contribution over lossy links, port 8890/udp.

   Watching in the browser (WebRTC and HLS) is on already and needs no tick.

Press **Create admin and finish setup**. You are signed in and land on the **Dashboard**. Continue with
[First steps](first-steps.md).

> [!TIP]
> Right after setup, invite a second admin on the **People** page, or at least keep a note of how to reset a
> password from the server ([First steps](first-steps.md#invite-a-second-admin)). With a single admin, a lost password
> can only be reset with a command on the server.

## Tip: try with Let's Encrypt's staging server first

Let's Encrypt limits how often it issues certificates and how many failed attempts it accepts for one name. If you
are still sorting out DNS or the firewall, use its staging server, which has generous limits. Its certificates are
not trusted by browsers, so expect a warning page that you click through. Add this line to `.env` (or remove the `#`
in front of the one that is there):

```
ACME_DIRECTORY=https://acme-staging-v02.api.letsencrypt.org/directory
```

and apply it:

```bash
docker compose up -d
```

Once the staging certificate works (the log shows no warning), remove the line again and run `docker compose up -d`
once more. The sidecar then gets a real certificate; it keeps the two apart, so the staging one is not reused.

## Where your data lives

Docker keeps the stack's data in **named volumes**, storage areas it manages itself, which survive when containers
are recreated or upgraded. List them:

```bash
docker volume ls
```

| Volume | Holds |
|---|---|
| `mediamtx-ui_data-state` | The database (people, streams, keys, history, audit log), the generated secrets, the certificates |
| `mediamtx-ui_data-config` | MediaMTX's configuration, written by the sidecar |
| `mediamtx-ui_data-recordings` | Recordings (unless `RECORDINGS_PATH` is set) |
| `mediamtx-ui_data-backups` | Encrypted backups (unless `BACKUPS_PATH` is set) |
| `mediamtx-ui_data-logs` | MediaMTX's log files |
| `mediamtx-ui_data-holding` | Holding-screen clips |
| `mediamtx-ui_data-hooks` | Scripts MediaMTX may run on events |

They live on the disk where Docker keeps its data (usually `/var/lib/docker`). `docker compose down` keeps them;
only `docker compose down -v` deletes them, along with everything in them ([Uninstall](uninstall.md)).

Backups are made only once you set a backup passphrase ([Backups](backups.md)); download one now and then, so that a
copy exists off the server.

### Recordings and backups on a disk of your choice

To keep recordings or backups on another disk (a big data disk, a USB SSD on a Raspberry Pi), create a directory
that belongs to the user the containers run as (uid 10002), for example:

```bash
sudo install -d -o 10002 -g 10002 -m 750 /srv/recordings
```

```bash
sudo install -d -o 10002 -g 10002 -m 750 /srv/mediamtx-backups
```

Then set them in `.env`:

```
RECORDINGS_PATH=/srv/recordings
BACKUPS_PATH=/srv/mediamtx-backups
```

and apply it:

```bash
docker compose up -d
```

The paths must be absolute (start with `/`). Do this before you record anything: recordings already in the volume
are not moved.

## Behind your own reverse proxy

If a web server such as Caddy, nginx or Traefik already uses ports 80 and 443 on the machine, let it handle HTTPS for
MediaMTX UI too. The sidecar then serves plain HTTP on `127.0.0.1:8080` and gets no certificate of its own. See
[Behind your own reverse proxy](behind-a-proxy.md).

## Closing stream ports when not needed (advanced)

By default the stream ports are open to anyone who can reach the server, and keys decide who may stream or watch.
Exposure control keeps them closed except while they are needed, but it needs a particular firewall setup: see
[Exposure control](exposure-control.md).

## Next steps

- [First steps](first-steps.md): a tour of the UI, your first stream, a second admin, backups.
- [Encoders](encoders.md): OBS, ffmpeg and cameras in detail.
- [Backups](backups.md): set a passphrase now.
- [Upgrading](upgrading.md): how to move to a new release.
- [Troubleshooting](troubleshooting.md): when something does not work.
- [Security](security.md): what protects the server, and the decisions to know about.
