# Exposure control (advanced)

By default the stream ports (RTSP 8554/tcp, RTMP 1935/tcp, SRT 8890/udp, WebRTC 8189/udp) are simply published: whoever
can reach the host can connect, and MediaMTX UI's authentication decides who may publish or watch. That is enough for
most installs. Close the ports you do not use in your provider's firewall.

Exposure control goes further: the stream ports stay closed to the internet until an admin opens them on the
**Exposure** page (for named addresses or everyone, until a time), and an encoder's address is let in automatically
while its stream page is open (and remembered for 30 days after a successful stream). Everything opened expires.

## What it needs

- **A firewall that ufw rules control and that sits in front of Docker's published ports.** On an ordinary Docker
  host this is *not* the case: Docker inserts its own iptables rules ahead of ufw, so ufw rules neither open nor close
  published ports. It works where ufw rules are mirrored into a firewall outside the host (a cloud firewall kept in
  sync from ufw), or where Docker's traffic is routed through ufw (for example with
  [ufw-docker](https://github.com/chaifeng/ufw-docker)). Make sure, before relying on it, that a port without a ufw rule
  really is closed from outside.
- **The host helper `mtx-portgate`**, which runs as root from systemd: the sidecar never gets any privilege on the
  host. The sidecar writes the wanted state into a request directory; the helper checks it against a root-owned policy
  (which ports, how long, how wide a source range) and changes only ufw rules whose comment starts with
  `mtx-portgate:`. It reports back through a status directory the sidecar can only read.

```
sidecar (uid 10002)                         host (root)
/data/portgate/desired.json  ──(path unit)──▶  mtx-portgate apply  ──ufw allow / delete──▶  firewall
/data/portgate-status/ (ro)  ◀──status.json──  /var/lib/mtx-portgate
```

## Install

On the Docker host, from a checkout of this repository (the binary is taken from the image the stack runs):

```bash
sudo deploy/portgate/install.sh ghcr.io/greatmastix/mediamtxui:latest
```

It installs `/usr/local/sbin/mtx-portgate`, the policy `/etc/mtx-portgate/policy.json` (kept if it exists;
`FORCE_POLICY=1` replaces it), the request directory `/var/lib/mtx-portgate-requests` (owned by uid 10002; set
`REQUESTS_DIR` for another) and the units `mtx-portgate.{service,path,timer}`, then runs it once: every port closed.

Then start the stack with the exposure override:

```bash
docker compose -f compose.yaml -f deploy/compose.exposure.yaml up -d
```

(`PORTGATE_DIR` and `PORTGATE_STATUS_DIR` in `.env` if you changed the directories.) The Exposure page appears for
admins.

## Operating it

```bash
sudo mtx-portgate status                     # what is open, from where, until when
sudo mtx-portgate close-all                  # panic button: works without the sidecar
sudo ufw show added | grep mtx-portgate:     # the rules it owns
journalctl -u mtx-portgate                   # what it did
```

Change the limits by editing `/etc/mtx-portgate/policy.json` (`sudo mtx-portgate check-policy` validates it); the UI
reads them from the helper's status. A `verify` block in the policy runs a command after each change and reports the
ports open only once its output contains the expected text (for example a cloud-firewall sync tool's status).

`sudo deploy/portgate/uninstall.sh` closes everything the helper opened and removes it (the policy stays).
