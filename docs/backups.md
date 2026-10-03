# Backups and restore

This page explains MediaMTX UI's encrypted backups: how to switch them on, what they contain, how to keep copies off
the server, how to restore one, and how to move your install to a new server. It is for admins; only admins see the
**Backups** page.

## The short version

1. Open **Backups**, set a passphrase (at least 12 characters), and keep it somewhere safe.
2. A backup is now made every night, and whenever you press **Back up now**.
3. Download one now and then and keep it somewhere other than the server.
4. To restore: **Restore from a file…** (or **Restore…** next to a kept backup), enter its passphrase, **Check it**,
   read what will change, **Restore**.

## Setting the passphrase

Backups are encrypted, and only the passphrase opens them. Until a passphrase is set, no backups are made.

1. Open **Backups** in the menu.
2. Under **Passphrase**, type the passphrase into **Passphrase** and again into **Again**. It must be at least 12
   characters.
3. Press **Set passphrase**, and confirm it's you (your password, an authenticator code or a passkey).

> [!WARNING]
> **The passphrase cannot be recovered.** Nobody can open a backup without it: not you, not this server, not the
> developers. If it is lost, so are the backups. Write it down or put it in a password manager, somewhere that does
> not depend on this server.

You can change it later with **Change passphrase**. Backups made from then on open with the new passphrase; older
backups keep the passphrase they were made with.

## Nightly and manual backups

Once a passphrase is set, the **Schedule** section appears:

| Setting | Default | Meaning |
|---|---|---|
| **Back up every day** | on | Make a backup once a day by itself |
| **At (UTC)** | 03:30 | When, in UTC; the page shows what that is in your own time |
| **Keep the newest** | 7 | How many of these daily backups to keep (1 to 365); older ones are deleted |

Changing the schedule asks you to confirm it's you.

**Back up now** (under **Kept backups**) makes a backup at once. Backups made this way are kept until you delete
them; the **Keep the newest** limit applies only to the daily ones.

Below the schedule, the page says when the last backup was made, or why it failed.

The **Kept backups** table lists every backup on the server, newest first, with when it was **Made**, its **Kind**
(**Scheduled**, **Made by hand**, **Before a restore**, **Uploaded**), its **Size**, and which server it is
**From**. Each row has a download button, **Restore…**, and a delete button (deleting asks you to confirm it's you).

Two more kinds are cleaned up by themselves: the newest three **Before a restore** backups are kept, and an
**Uploaded** file is deleted after a day (at most three are kept).

## What a backup contains

| In a backup | Not in a backup |
|---|---|
| People, their passwords, authenticator apps and passkeys | Recordings |
| Streams, their keys and settings, guest keys, forwards | MediaMTX's log and the sidecar's log |
| Stream credentials (and the key they are encrypted with) | The HTTPS certificate (a new one is requested by itself) |
| The MediaMTX configuration (`mediamtx.yml`) and its full history | Your `.env`, `compose.yaml` and overrides |
| Holding clips | |
| Chart history and the audit log | |
| The backup passphrase and schedule | |

In short: everything needed to set this server up again, except the recordings. Copy recordings separately if you
need them (see [Backing up the Docker volumes](#backing-up-the-docker-volumes)), and keep a copy of your `.env` and
any `compose.override.yaml` yourself.

## Where backups are kept

On the server, in the sidecar's `backups/` directory. By default that is a Docker volume named
`mediamtx-ui_data-backups`. To keep them on a disk of your choice, set `BACKUPS_PATH` in `.env` to an absolute path
that belongs to user 10002 (the user the containers run as):

```bash
sudo install -d -o 10002 -g 10002 -m 750 /srv/mediamtx-backups
```

```bash
BACKUPS_PATH=/srv/mediamtx-backups
```

Then run `docker compose up -d`. Backups already in the volume are not moved; download any you want to keep first.

A backup on the same disk as the server does not help if the server is lost. Keep copies elsewhere.

## Keeping copies off the server

**From the browser:** press the download button in a backup's row. You get a file named like
`mtxui-20261003-033000-scheduled.mtxbackup`. It is encrypted, so it is safe to keep in cloud storage or on a USB
stick, as long as the passphrase is kept separately.

**From the command line:** copy the whole backups directory out of the sidecar container. This works whether
backups are in the Docker volume or in `BACKUPS_PATH`. On the server, in the directory with `compose.yaml`:

```bash
docker compose cp sidecar:/data/backups ./backups-copy
```

Then, on your own computer, fetch that copy (replace the user and server name with yours):

```bash
scp -r you@stream.example.com:mediamtx-ui/backups-copy ./mediamtx-ui-backups
```

You can run these two steps regularly from a script or a cron job to keep a copy off the server.

## Restore

A restore puts the server back to the moment the backup was made. It works on the same server, or on a fresh one
(see [Moving to a new server](#moving-to-a-new-server)).

1. Open **Backups**.
2. Choose the backup:
   - one that is on the server: press **Restore…** in its row;
   - a file on your computer: press **Restore from a file…** and pick the `.mtxbackup` file. It is uploaded, and
     appears in the list as **Uploaded**.
3. Type **The backup's passphrase** and press **Check it**. The server opens the backup and checks every part of it,
   including its `mediamtx.yml` against the same rules as an edit in the UI; nothing changes yet. A wrong passphrase
   gives "That passphrase does not open this backup." If a part of the backup is refused, the message says why.
4. **Restore this backup?** shows what the restore would change: which people and streams come back and which go
   away again, how many stream credentials, configuration versions and holding clips it holds, whether
   `mediamtx.yml` changes, and how much chart history and audit log it brings.
5. Press **Restore** within 15 minutes of the check (after that, check it again), and confirm it's you.

What happens then:

- If this server has a backup passphrase, a **Before a restore** backup of the current state is made first. If that
  fails, nothing is restored.
- The sidecar restarts with the backup's data. This takes a few seconds; MediaMTX keeps running. Streams whose
  settings differ in the backup's `mediamtx.yml` restart, which drops their viewers for a moment.
- Everyone is signed out and signs in again with the passwords from the backup.
- The audit log is the backup's, with the entries made since the backup added to it, and an entry for the restore
  itself: a restore never takes anything out of the audit log.
- The backup passphrase and schedule are the backup's from now on.

If the restored server does not start properly, the sidecar puts the previous database back by itself on the next
start, and the audit log records the restore as failed.

> [!NOTE]
> A backup can be restored by the version of MediaMTX UI that made it or a newer one, never an older one. An older
> server refuses it with "it comes from a newer version of the sidecar … update first": [upgrade](upgrading.md) the
> server, then restore.

> [!NOTE]
> Restoring does not help with a lost password: it brings back the same accounts and passwords. See
> [Lost password](troubleshooting.md#lost-password-or-authenticator).

## Moving to a new server

The backup file and its passphrase are all a new server needs. The restore replaces everything on the new server,
including the admin account you create there during setup.

1. **On the old server**, open **Backups**, press **Back up now**, and download that backup. Write down its
   passphrase if you have not already. Also keep a copy of your `.env` and any `compose.override.yaml` or other
   overrides.
2. **Point your domain at the new server.** Change the A record (see [Before you start](before-you-start.md)) to the
   new server's address. The new server needs this to get its HTTPS certificate. After this, the domain no longer
   reaches the old server, which is why you download the backup first.
3. **Install MediaMTX UI on the new server** as in [Install](install.md), with the same `DOMAIN` in `.env` (or copy
   your old `.env` over). Use the same release as the old server, or a newer one.
4. **Finish the setup wizard** with the setup token. The account you create here is temporary: the restore replaces
   it with the people from the backup. Any username and a strong password will do; you need it once more in step 6.
5. Open **Backups**. You do not need to set a passphrase on the new server. Press **Restore from a file…** and pick
   the backup.
6. Type the backup's passphrase, press **Check it**, read the preview, press **Restore**, and confirm it's you with
   the temporary account's password.
7. When the page goes to sign-in, sign in with your **old** account. The temporary account is gone.
8. **Recordings** are not in the backup. If you need them, copy the recordings volume from the old server
   separately (see below).
9. If you use [exposure control](exposure-control.md), install the host helper on the new server too.

Stream keys and watch links stay the same, so encoders and viewers need no changes as long as the domain is the same.

If the new server has a **different domain**, the restore still works, but:

- encoders need the new server address (the stream page shows it), and old watch links stop working;
- passkeys stop working, because a passkey belongs to the domain it was created on. Sign in with your password (and
  authenticator code), then remove the old passkey and add a new one on your **Account** page.

> [!NOTE]
> The check applies the same rules to the backup's `mediamtx.yml` as to an edit in the UI. One case catches people
> out: hooks (`runOn…` settings), which the UI cannot add or change. If they were added to the old server's
> `mediamtx.yml` by hand, the check on a server without them refuses the backup with "The backup's mediamtx.yml is
> refused: Hooks run commands inside the MediaMTX container …".

## Upload size limit

The server accepts backup files up to 2048 MB for a restore. Backups without recordings are usually far smaller. To
raise the limit, set `MTXUI_BACKUP_MAX_UPLOAD_MB` in a `compose.override.yaml` (see [All settings](config.md)):

```yaml
services:
  sidecar:
    environment:
      MTXUI_BACKUP_MAX_UPLOAD_MB: "4096"
```

If you run your own reverse proxy, it must also allow uploads that large (see
[Behind a reverse proxy](behind-a-proxy.md)).

## Backing up the Docker volumes

The UI's backups are the easy and safe way. As an alternative, or in addition (for example to include the
recordings, or to have a complete copy before an upgrade), you can copy the Docker volumes themselves. This copy is
**not** encrypted: it contains the database and the server's secrets, so keep it safe.

> [!WARNING]
> Stop the stack first. Copying the database while the sidecar is writing to it can give you a broken copy.

The volumes are:

| Volume | Holds |
|---|---|
| `mediamtx-ui_data-state` | The database, secrets and the HTTPS certificate |
| `mediamtx-ui_data-config` | `mediamtx.yml` |
| `mediamtx-ui_data-holding` | Holding clips |
| `mediamtx-ui_data-hooks` | Hook scripts |
| `mediamtx-ui_data-recordings` | Recordings (unless you set `RECORDINGS_PATH`) |
| `mediamtx-ui_data-backups` | The UI's backups (unless you set `BACKUPS_PATH`) |
| `mediamtx-ui_data-logs` | MediaMTX's log |

In the directory with `compose.yaml`, stop the stack:

```bash
docker compose stop
```

Copy one volume into a compressed file in the current directory (repeat for each volume you want, changing the name
in both places):

```bash
docker run --rm -v mediamtx-ui_data-state:/volume:ro -v "$PWD":/backup alpine tar -czf /backup/data-state.tar.gz -C /volume .
```

Start the stack again:

```bash
docker compose start
```

To put a volume back later, stop the stack, then unpack the file into the volume. This replaces what is in it:

```bash
docker run --rm -v mediamtx-ui_data-state:/volume -v "$PWD":/backup alpine sh -c "rm -rf /volume/* && tar -xzf /backup/data-state.tar.gz -C /volume --numeric-owner"
```

If you set `RECORDINGS_PATH` or `BACKUPS_PATH`, those are ordinary directories on the host: copy them with your
usual tools.
