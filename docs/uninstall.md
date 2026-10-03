# Uninstall

This page explains how to stop MediaMTX UI and remove it from a server, step by step: just stopping it, removing
the containers, deleting the data, and removing the images. Each step goes further than the one before; stop
wherever you want. It is for whoever runs the server.

All commands run in the directory with your `compose.yaml` and `.env` (usually `~/mediamtx-ui`):

```bash
cd ~/mediamtx-ui
```

If your `.env` has a `COMPOSE_FILE=` line (for an override), `docker compose` reads it by itself, so the commands
below work as written.

## Before you start: keep a backup

Deleting the data cannot be undone. If you might want anything back, or plan to set up MediaMTX UI again elsewhere,
first open **Backups** in the UI, press **Back up now**, and download that backup (see [Backups](backups.md)). Keep
its passphrase with it. Recordings are not in a backup: download the ones you want on the **Recordings** page, or
copy the volume (see [Backing up the Docker volumes](backups.md#backing-up-the-docker-volumes)).

## 1. Stop it

Stop both containers without removing anything:

```bash
docker compose stop
```

Streams, the UI and the stream ports stop. Everything stays on disk, and `docker compose start` brings it back as it
was. The containers have the restart policy `unless-stopped`, so stopped containers stay stopped after a reboot.

## 2. Remove the containers

Remove the containers and the stack's network:

```bash
docker compose down
```

Your data stays in its Docker volumes, so `docker compose up -d` later starts where you left off.

## 3. Remove the exposure helper

Only if you set up [exposure control](exposure-control.md): remove the host helper before you delete the data. From
your checkout of the repository:

```bash
sudo deploy/portgate/uninstall.sh
```

It stops the helper, closes every port it opened in ufw, and removes it. Its policy in `/etc/mtx-portgate/` stays;
delete that directory yourself if you no longer want it. See [Exposure control](exposure-control.md#uninstall).

## 4. Delete the data

> [!WARNING]
> This deletes people, streams, keys, the configuration, the certificate, and the recordings and backups kept in
> Docker volumes. It cannot be undone.

Remove the containers together with the stack's volumes:

```bash
docker compose down -v
```

To check that they are gone, list the volumes that are left; nothing starting with `mediamtx-ui_` should appear:

```bash
docker volume ls
```

**Directories you set yourself are not deleted.** If `.env` sets `RECORDINGS_PATH` or `BACKUPS_PATH`, those are
ordinary directories on the host, and `docker compose down -v` leaves them alone. Delete them yourself once you are
sure you do not need them (with your own path):

```bash
sudo rm -rf /srv/recordings
```

Then the directory with `compose.yaml`, `.env` and any overrides can go too. `.env` holds no passwords or secrets,
but keep a copy if you might install again with the same settings:

```bash
rm -rf ~/mediamtx-ui
```

## 5. Remove the images

The downloaded images stay on disk until you remove them. List them:

```bash
docker image ls
```

Remove MediaMTX UI's sidecar images (every version):

```bash
docker image ls ghcr.io/greatmastix/mediamtxui -q | xargs -r docker image rm
```

Remove the MediaMTX images, unless something else on the host uses MediaMTX:

```bash
docker image ls bluenviron/mediamtx -q | xargs -r docker image rm
```

## 6. Close the ports

Close the ports you opened for MediaMTX UI in your provider's firewall or your router's port forwarding: TCP 80 and
443 (unless something else on the server uses them), and the stream ports TCP 1935, TCP 8554, UDP 8890 and UDP 8189.

Finally, if the domain was only for this server, remove its A record or point it elsewhere.

## Docker itself

Removing MediaMTX UI does not remove Docker. If you installed Docker only for this, see Docker's own instructions
for [uninstalling Docker Engine](https://docs.docker.com/engine/install/ubuntu/#uninstall-docker-engine) (pick your
Linux distribution there).
