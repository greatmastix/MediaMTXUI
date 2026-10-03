# Install script

The quickest way from a fresh Linux machine (Ubuntu, Debian, Raspberry Pi OS, Fedora) to a running MediaMTX UI: one
command installs Docker if it is missing, asks how people will reach the server, starts everything and prints the
address to open and the setup token. Running it again later upgrades to the newest release.

## Run it

Open a terminal on the machine ([how](install-docker.md#open-a-terminal-on-the-server)) and run:

```bash
curl -fsSL https://github.com/greatmastix/MediaMTXUI/releases/latest/download/install.sh | sudo bash
```

(No `curl` yet? `sudo apt install curl` first.)

It asks one question, and one or two more depending on the answer:

1. **How will people reach it?**
   - **1, on the internet**: at a domain name, with HTTPS from Let's Encrypt. It asks for the domain (and an optional
     e-mail address for expiry warnings) and checks that the domain points at this machine. You need the domain's A
     record and ports 80 and 443 open first: see [Before you start](before-you-start.md).
   - **2, on this local network only**: at `http://<address>:8080`, nothing reachable from the internet. It suggests
     this machine's address; press Enter to take it.
2. **Run docker without sudo?** At the end it offers to add you to the `docker` group, so that `docker compose`
   commands work without `sudo` after you log in again. (That group can do anything root can; on a machine that is
   only for streaming, that is usually fine.)

When it is done, it prints something like:

```
Done. Open http://192.168.1.50:8080
  Setup token: ABCD-EFGH-JKLM-NPQR
  Install folder: /opt/mediamtx-ui (run docker compose commands there)
```

Open the address, enter the token, and create the first admin ([the setup wizard](install.md#8-the-setup-wizard)).
Then continue with [First steps](first-steps.md).

## What it does

1. Checks the machine: Linux, run as root, an architecture with an image (amd64, arm64, armv7).
2. Installs `curl` and Docker with the Compose plugin if they are missing, with Docker's own install script
   ([Install Docker](install-docker.md)), and starts Docker on boot.
3. Asks the questions above, and refuses if ports it needs (80 and 443 or 8080, 1935, 8554) are in use already.
4. Downloads the release's `compose.yaml` and `env.example` (as `.env`) into `/opt/mediamtx-ui`, plus
   `compose.lan.yaml` for a local install, and writes your answers into `.env`.
5. Downloads the images, starts the stack, and waits until it is healthy. A public install also requests its
   certificate right away and says whether that worked.
6. Prints the address and the setup token.

The result is the same as the step-by-step [Install](install.md) or [Local network only](install-local.md): the same
files and settings, so everything in these docs applies. Run `docker compose` commands in `/opt/mediamtx-ui`:

```bash
cd /opt/mediamtx-ui
```

## Upgrade

Run the same command again:

```bash
curl -fsSL https://github.com/greatmastix/MediaMTXUI/releases/latest/download/install.sh | sudo bash
```

It finds the existing install, downloads the newest release's compose files (keeping your `.env`), pulls the images
and restarts. Make a backup first (**Backups** page, **Back up now**). Details: [Upgrading](upgrading.md).

## Options

To answer the questions in advance (for example in your own scripts), pass options after `bash -s --`:

```bash
curl -fsSL https://github.com/greatmastix/MediaMTXUI/releases/latest/download/install.sh | sudo bash -s -- --local --yes
```

| Option | Meaning |
|---|---|
| `--public DOMAIN` | On the internet at `https://DOMAIN` |
| `--email ADDRESS` | E-mail for Let's Encrypt's expiry warnings (public) |
| `--staging` | Let's Encrypt's staging server, for a first try (public; browsers warn about its certificate) |
| `--local [ADDRESS]` | On the local network, at `http://ADDRESS:8080` (default: this machine's address) |
| `--dir PATH` | Another install folder than `/opt/mediamtx-ui` |
| `--version v1.2.3` | A particular release instead of the newest |
| `--yes` | No questions: take the defaults, fail where there is none |

## Read it first

Piping a script from the internet into `sudo bash` runs it as root without showing it to you. To look at it first,
download it, read it, then run it:

```bash
curl -fsSL -o install.sh https://github.com/greatmastix/MediaMTXUI/releases/latest/download/install.sh
```

```bash
less install.sh
```

```bash
sudo bash install.sh
```

The script is `scripts/install.sh` in the repository; each release attaches the version it was tested with.
