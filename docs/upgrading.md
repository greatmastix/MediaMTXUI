# Upgrading

This page explains how to move an existing MediaMTX UI install to a newer release, how to stay on a particular
version, and what to do if an upgrade goes wrong. It is for whoever looks after the server.

## The short version

In the directory with your `compose.yaml` and `.env` (usually `~/mediamtx-ui`):

```bash
curl -fsSLO https://github.com/greatmastix/MediaMTXUI/releases/latest/download/compose.yaml
```

```bash
docker compose pull && docker compose up -d
```

If you use one of the optional overrides (your own reverse proxy, local network only, exposure control), download
the new copy of that file too, as described [below](#4-if-you-use-overrides). Make a backup first: see
[Before you start](#1-make-a-backup).

## Why you download compose.yaml

MediaMTX UI runs two containers: MediaMTX itself (the streaming server) and the *sidecar* (the web UI and
everything around it). Each release of the sidecar is built and tested against one exact MediaMTX version. The
release's `compose.yaml` names both images: the sidecar of that release, and the MediaMTX it was tested with.

So you upgrade by replacing `compose.yaml` with the new release's copy. Never upgrade one image on its own:
`docker compose pull` by itself only fetches the versions your current `compose.yaml` already names, so it does not
upgrade anything. If the two ever do not match, every page shows a warning (see
[Troubleshooting](troubleshooting.md#warnings-at-the-top-of-the-page)).

Your own settings are safe during this, because they do not live in `compose.yaml`:

| File | What it holds | During an upgrade |
|---|---|---|
| `compose.yaml` | The two images and how they run | Replaced with the new release's copy |
| `.env` | Your domain and the settings from `env.example` | Keep it |
| `compose.override.yaml` | Any further settings of your own (see [All settings](config.md)) | Keep it |
| `compose.behind-proxy.yaml`, `compose.lan.yaml`, `compose.exposure.yaml` | Optional overrides from the docs | Replace with the new release's copy |

Everything else (people, streams, keys, the configuration, recordings) is in Docker volumes and stays where it is.

## Step by step

### 1. Make a backup

Open the **Backups** page in the UI and press **Back up now**, then download that backup with the download button in
its row (see [Backups](backups.md)). If you have not set a backup passphrase yet, set one first: backups start only
once a passphrase is set.

Database changes in a new release only go forward, so a backup made *before* the upgrade is your way back. For a
guaranteed way back, you can also copy the Docker volumes themselves (see
[Backing up the Docker volumes](backups.md#backing-up-the-docker-volumes)).

### 2. Read the release notes

The [releases page](https://github.com/greatmastix/MediaMTXUI/releases) lists every release with what changed.
Skim the notes of every release between yours and the new one. To find out which version you run:

```bash
docker compose exec sidecar /mtxui version
```

```
mtxui 1.2.3 (built for MediaMTX 1.21.1)
```

> [!TIP]
> To hear about new releases, open the repository on GitHub, click **Watch**, choose **Custom** and tick
> **Releases**.

### 3. Download the new compose.yaml

Go to the directory with your `compose.yaml` and `.env`:

```bash
cd ~/mediamtx-ui
```

Download the latest release's `compose.yaml` over your old one:

```bash
curl -fsSLO https://github.com/greatmastix/MediaMTXUI/releases/latest/download/compose.yaml
```

`-O` saves it under its own name, `compose.yaml`, which replaces the old file. Do not edit it: settings of your own go
into `.env` or `compose.override.yaml`.

### 4. If you use overrides

If your `.env` has a `COMPOSE_FILE=` line, it lists the overrides you use. Download the new copy of each one the
same way. Only download the ones you actually use:

```bash
curl -fsSLO https://github.com/greatmastix/MediaMTXUI/releases/latest/download/compose.behind-proxy.yaml
```

```bash
curl -fsSLO https://github.com/greatmastix/MediaMTXUI/releases/latest/download/compose.lan.yaml
```

```bash
curl -fsSLO https://github.com/greatmastix/MediaMTXUI/releases/latest/download/compose.exposure.yaml
```

### 5. Pull the new images and restart

Fetch the images the new `compose.yaml` names:

```bash
docker compose pull
```

Then recreate the containers with them:

```bash
docker compose up -d
```

Docker replaces the containers whose image changed. The sidecar's database is upgraded by itself when the new
sidecar starts. Streams are interrupted while MediaMTX restarts, so pick a quiet moment; encoders such as OBS usually
reconnect by themselves.

### 6. Check that it worked

```bash
docker compose ps
```

Both containers should be `Up`, and the sidecar should say `(healthy)` after a few seconds:

```
NAME                     IMAGE                                      STATUS
mediamtx-ui-mediamtx-1   bluenviron/mediamtx:1.21.1@sha256:…        Up 1 minute
mediamtx-ui-sidecar-1    ghcr.io/greatmastix/mediamtxui:1.2.4       Up 1 minute (healthy)
```

Then check the version again:

```bash
docker compose exec sidecar /mtxui version
```

Open the UI and sign in. No warning about the MediaMTX version should appear at the top of the page. If something is
wrong, the sidecar's log says why:

```bash
docker compose logs --tail=100 sidecar
```

### 7. Exposure control: install the helper again

Only if you use [exposure control](exposure-control.md): the host helper `mtx-portgate` is copied out of the
sidecar's image, so install it again after every image update, so that helper and sidecar understand the same file
formats. From an up-to-date checkout of the repository, naming the image your stack now runs (the version from step
6):

```bash
sudo deploy/portgate/install.sh ghcr.io/greatmastix/mediamtxui:1.2.4
```

See [Exposure control](exposure-control.md#install) for the details.

## Upgrading to a particular release

To move to a release other than the latest, download from that release instead of `releases/latest/`. For version
1.2.3:

```bash
curl -fsSLO https://github.com/greatmastix/MediaMTXUI/releases/download/v1.2.3/compose.yaml
```

The overrides are available from the same address. Then continue with `docker compose pull` and
`docker compose up -d` as above.

## Staying on a version

You do not need to do anything to stay on a version: the release's `compose.yaml` names an exact sidecar version and
an exact MediaMTX version, so nothing changes until you download a new `compose.yaml`. `docker compose pull` on its
own does not upgrade you.

The `MTXUI_VERSION` setting in `.env` exists to run a *different* sidecar build than the one `compose.yaml` names.
You normally leave it unset. If you set it, keep in mind that `compose.yaml` still names its own MediaMTX version,
and another sidecar version may expect another MediaMTX. The sidecar images are tagged `1.2.3`, `1.2`, `1`, `latest`
and `edge`.

## The edge image (for testers)

`edge` is the sidecar built from the newest code on the `main` branch, published on every change. It has not been
through a release and may be broken on any given day. Use it only on a test server, to try a fix before it is
released.

To run it, take `compose.yaml` from the `main` branch instead of a release. On `main`, the file names `edge` and the
MediaMTX version `main` is tested with:

```bash
curl -fsSL -o compose.yaml https://raw.githubusercontent.com/greatmastix/MediaMTXUI/main/compose.yaml
```

```bash
docker compose pull && docker compose up -d
```

Going back from `edge` to a release counts as a downgrade (see the next section) if `main` has changed the database
in the meantime.

## Downgrading

Going back to an older release is not supported once the newer one has started. When a new sidecar starts, it
upgrades the database to its own format, and an older sidecar does not understand that format. Do not just put the
old `compose.yaml` back: the old sidecar does not refuse a database from a newer version, and it may then misbehave.

You have two ways back:

- **A copy of the Docker volumes made before the upgrade** (see
  [Backing up the Docker volumes](backups.md#backing-up-the-docker-volumes)). Stop the stack, put the copy back,
  put the old `compose.yaml` back, and start it. Everything is as it was at the moment of the copy.
- **A backup made before the upgrade**, on a fresh install of the old release. A backup can be restored by the
  version that made it or a newer one, never an older one: an older sidecar refuses it with "it comes from a newer
  version of the sidecar … update first". See [Restore](backups.md#restore) and
  [Moving to a new server](backups.md#moving-to-a-new-server). Note that a fresh install starts with empty volumes.

If an upgrade fails, please [report it](troubleshooting.md#reporting-a-problem) as well, so that it can be fixed.

## Cleaning up old images

Old images stay on disk after an upgrade. To see what is there:

```bash
docker image ls
```

`docker image prune` removes only *dangling* images (ones without a name). The old MediaMTX UI and MediaMTX images
still carry their version tags, so they are not dangling. To remove every image that no container uses:

```bash
docker image prune -a
```

> [!WARNING]
> `docker image prune -a` removes **every** unused image on this host, not only MediaMTX UI's. Run it while the stack
> is running (so its images are in use and kept). If other projects on the host have stopped containers, their images
> are kept too, but images of projects that are not running at all are removed and would be downloaded again.

To remove one particular old image instead, name it (with the tag shown by `docker image ls`):

```bash
docker image rm ghcr.io/greatmastix/mediamtxui:1.2.3
```
