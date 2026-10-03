# Exposure control (advanced)

By default the stream ports (RTSP 8554/tcp, RTMP 1935/tcp, SRT 8890/udp, WebRTC 8189/udp) are simply published: whoever
can reach the host can connect, and MediaMTX UI's authentication decides who may publish or watch. That is enough for
most installs. Close the ports you do not use in your provider's firewall.

Exposure control goes further: the stream ports are closed to the internet except while something opens them, and
everything opened expires. Openings come from two places:

- **Manual openings** on the **Exposure** page (admins only): a port for named addresses or for everyone, until a time.
- **Automatic rules**, all three **on by default**; an admin switches each off on the Exposure page. Only protocols
  switched on in MediaMTX are opened.
  - *Let encoders in while a stream is set up or live*: while a stream's page is open, the stream's publishing ports
    (RTMP, SRT, RTSP, WebRTC media) accept the address the sidecar sees for that browser (not the encoder's, unless
    they are the same machine; at most three addresses per stream). While a stream is live, the port its encoder came
    through stays open to the encoder's address. While a guest publish key is valid, the publishing ports are open
    to **anyone**, since a guest's address is unknown.
  - *Remember encoders for 30 days*: an address that streamed successfully keeps getting in for 30 days after its last
    stream (three per stream).
  - *Let players and viewers in while something is live*: while any path is live, or any stream has a holding screen,
    RTSP, RTMP, SRT and WebRTC media are open to **anyone**, so outside players reach them.

So with the defaults, the four stream ports are open to everyone whenever anything is live, and any stream's manager,
a streamer included, opens them to everyone by setting a holding screen or making a guest publish key. Keys and
MediaMTX UI's authentication still decide who may publish or watch. If the ports should only ever open to known
addresses, switch the viewers rule off, and the encoders rule too unless you can do without guest publish keys.

## What it needs

- **A firewall that ufw's own rules control and that sits in front of Docker's published ports.** On an ordinary
  Docker host this is *not* the case: Docker inserts its own iptables rules ahead of ufw, so ufw rules neither open nor
  close published ports. It works where ufw's rules are mirrored into a firewall outside the host (a cloud firewall
  kept in sync from ufw). It does *not* work with [ufw-docker](https://github.com/chaifeng/ufw-docker) or other setups
  that filter Docker's traffic with `ufw route` rules: the helper writes `ufw allow` rules, which traffic to a
  container never meets, so the ports would stay closed whatever the Exposure page says. Make sure, before relying on
  it, that a port without a ufw rule really is closed from outside, and that one with a rule is open.
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
`REQUESTS_DIR` for another) and the units `mtx-portgate.{service,path,timer}`, then runs it once: every port closed
until the sidecar asks. Run it again after updating the image, so that helper and sidecar speak the same file formats.

Then add the exposure override to the stack. Download it next to `compose.yaml`:

```bash
curl -fsSLO https://github.com/greatmastix/MediaMTXUI/releases/latest/download/compose.exposure.yaml
```

add this line to `.env`, so that every `docker compose` command (upgrades included) uses it (with your own reverse
proxy, list `compose.behind-proxy.yaml` before it):

```bash
COMPOSE_FILE=compose.yaml:compose.exposure.yaml
```

and run `docker compose up -d`. (In a checkout of the repository, the override is `deploy/compose.exposure.yaml`.)
Set `PORTGATE_DIR` and `PORTGATE_STATUS_DIR` in `.env` if you changed the directories. The Exposure page appears for
admins.

## Operating it

```bash
sudo mtx-portgate status                     # what is open, from where, until when
sudo mtx-portgate close-all                  # panic button: closes everything now, without the sidecar
sudo ufw show added | grep mtx-portgate:     # the rules it owns
journalctl -u mtx-portgate                   # what it did
```

`close-all` holds while the stack runs: the helper opens nothing again until the sidecar has taken it in, which the
sidecar does by itself within seconds, the way **Close all** on the Exposure page works: it closes the manual openings
and switches every automatic rule off (the audit log shows `exposure.close-all` by `system`). After that, ports open
again only when an admin opens them or switches a rule back on. With the stack stopped, everything stays closed until
the sidecar starts and has taken the close-all in.

Change the limits by editing `/etc/mtx-portgate/policy.json` (`sudo mtx-portgate check-policy` validates it); the UI
reads them from the helper's status. The helper reads the policy on every run, at least once a minute: what it opened
before and the new limits no longer allow closes, with the reason in its status. The sidecar then drops the manual
openings the limits refuse, shortens automatic openings to the longest the policy allows, and leaves out the ones it
refuses (an opening to anyone when `allowAnySource` is off). A `verify` block in the policy runs a command after each
change and reports the ports open only once its output contains the expected text (for example a cloud-firewall sync
tool's status).

### Your own ufw rules

ufw keeps one rule per port, protocol and source address: adding a rule that differs from an existing one only in its
action (allow, deny, reject, limit) or comment replaces that one. So the helper never adds a rule where one of yours
matches it, such as `ufw deny from 203.0.113.7 to any port 1935 proto tcp` for an address it would let in, or
`ufw allow 1935/tcp` for an opening to anyone: it leaves your rule as it is and reports the port in error on the
Exposure page, naming your rule. Your rules for other addresses, for every port, for any protocol, routed ones and
outgoing ones do not get in its way. On the stream ports, let the helper decide, or keep your rules to addresses it
does not open.

## Uninstall

`sudo deploy/portgate/uninstall.sh` stops the helper's units, closes everything it opened and removes it, together
with the sidecar's last request (the policy stays), so that installing it again starts with every port closed. It
fails if a rule marked `mtx-portgate:` is left. If you install the helper again while the stack runs, restart the stack
afterwards: the sidecar's mount of the status directory still points at the one uninstall removed.
