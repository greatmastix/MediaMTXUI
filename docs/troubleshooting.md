# Troubleshooting

This page is organised by symptom: find what you see, then work through the causes from the most likely one down. It
also explains every warning the UI can show at the top of the page, and how to collect what a bug report needs. It is
for whoever runs the server.

Almost every answer starts with the same two commands, run in the directory with your `compose.yaml` (usually
`~/mediamtx-ui`). The first shows whether the two containers are running:

```bash
docker compose ps
```

The second shows the sidecar's recent log, which says what it is doing and why something failed:

```bash
docker compose logs --tail=100 sidecar
```

For MediaMTX's own log, use `mediamtx` instead of `sidecar`. Once you can sign in, admins can read both logs on the
**Logs** page too (see [Logs and audit](logs-and-audit.md)).

> [!NOTE]
> If your `.env` has a `COMPOSE_FILE=` line, every `docker compose` command picks it up by itself. If you start the
> stack with `-f` options instead, use the same options for these commands too.

## Contents

- [The site does not load](#the-site-does-not-load)
- [Certificate problems](#certificate-problems)
- [The setup token](#the-setup-token)
- [Cannot sign in](#cannot-sign-in)
- [The encoder cannot connect](#the-encoder-cannot-connect)
- [Video does not play, or only after a delay](#video-does-not-play-or-only-after-a-delay)
- [Recordings stop](#recordings-stop)
- [A container keeps restarting](#a-container-keeps-restarting)
- [Warnings at the top of the page](#warnings-at-the-top-of-the-page)
- [Lost password or authenticator](#lost-password-or-authenticator)
- [Reporting a problem](#reporting-a-problem)

## The site does not load

The browser says it cannot connect, or the page times out.

**1. Are the containers running?**

```bash
docker compose ps
```

Both `mediamtx-ui-sidecar-1` and `mediamtx-ui-mediamtx-1` should be listed as `Up`, and the sidecar as `(healthy)`.
If nothing is listed, start the stack with `docker compose up -d`. If a container says `Restarting` or `Exited`, see
[A container keeps restarting](#a-container-keeps-restarting).

**2. Does the domain point at this server?** Your domain needs an A record with the server's public IPv4 address (see
[Before you start](before-you-start.md)). Check what the domain resolves to (replace the name with yours):

```bash
getent ahostsv4 stream.example.com
```

Compare it with the server's public address, which your hosting provider's dashboard shows, or:

```bash
curl -4 -s https://ifconfig.me
```

If they differ, fix the A record and wait for it to spread (usually minutes, sometimes up to a few hours). An AAAA
(IPv6) record alone does not work: MediaMTX UI publishes its ports on IPv4 only.

**3. Are ports 80 and 443 reachable from the internet?** Most cloud providers have a firewall ("security group")
that blocks everything not explicitly allowed. Allow TCP 80 and 443 there. At home, your router must forward TCP 80
and 443 to the server (port forwarding). Test from a machine *outside* your server's network, such as your phone on
mobile data:

```bash
curl -I http://stream.example.com
```

If it works you see `HTTP/1.1 301 Moved Permanently` with a `Location: https://…` line. A timeout means a firewall,
router or provider blocks the port.

> [!NOTE]
> On an ordinary Docker host, ufw (Ubuntu's firewall) does **not** control ports published by Docker: Docker's own
> rules come first. So `ufw allow 443` is not the fix, and `ufw deny` does not close a published port either. The
> firewall that matters is the one in front of the server (your provider's, or your router).

**4. Is something else using ports 80 or 443?** If another web server (nginx, Apache, Caddy) already runs on the host,
`docker compose up -d` fails with `port is already allocated` or `address already in use`. Either stop that server,
or let it forward to MediaMTX UI: see [Behind a reverse proxy](behind-a-proxy.md).

**5. Local network install?** With `compose.lan.yaml` the UI is at `http://DOMAIN:8080`, not on port 443. See
[Local network only](install-local.md).

## Certificate problems

The browser warns that the connection is not private, or the site does not load over HTTPS.

The sidecar requests its certificate from Let's Encrypt on the first start, and says in its log when it cannot get one:

```bash
docker compose logs sidecar | grep -i certificate
```

A line like `no certificate yet; check that the domain points here and ports 80 or 443 are reachable` comes with the
reason from Let's Encrypt. The usual causes:

- **The A record is wrong or not there yet.** Let's Encrypt checks your domain from the internet, so it must resolve
  to this server. See step 2 [above](#the-site-does-not-load).
- **Ports 80 and 443 are blocked.** Let's Encrypt connects to your server on port 80 or 443 to check that you control
  the domain. One of them is enough for the certificate, but keep both open: port 80 also redirects `http://`
  addresses to HTTPS. Some home internet providers block incoming port 80; then 443 must be open.
- **Rate limits.** Let's Encrypt limits how often it issues certificates for the same domain and how many failed
  attempts it accepts per hour. If the log mentions `rateLimited` or "too many", wait (the message says until when)
  rather than retrying. Retrying, or deleting the stack's volumes and starting again, makes it worse: the certificate
  is stored in the `data-state` volume, so removing that volume means requesting a new one.
- **You are using the staging server.** If `.env` contains `ACME_DIRECTORY=https://acme-staging-v02.api.letsencrypt.org/directory`,
  the certificate comes from Let's Encrypt's test server, which browsers do not trust. That is expected while trying
  things out. To switch to a real certificate, remove that line from `.env` and run `docker compose up -d`.

Once the cause is fixed, open the site again: the sidecar tries again when someone visits. If it still does not
work, restart it:

```bash
docker compose restart sidecar
```

If you run your own reverse proxy (`compose.behind-proxy.yaml`), the sidecar does not get a certificate at all: your
proxy does. Check the proxy's log instead.

## The setup token

The first page asks for a **Setup token**. It is a one-time code that proves you are the person who installed the
server. Show it with:

```bash
docker compose exec sidecar /mtxui setup-token
```

It looks like `ABCD-EFGH-…`. Dashes, spaces and upper or lower case do not matter when you type it. The token stays
the same across restarts until setup is done. The sidecar also prints it in its log at each start while setup is
pending.

- `no setup token: setup is done (or the sidecar has not started yet)` means either someone already finished the
  setup (go to the sign-in page instead), or the sidecar is not running yet (check `docker compose ps`).
- "The setup token is wrong" means it was mistyped. Copy it again.
- "Too many attempts. Try again in … seconds." means too many wrong tries; wait and try again.

## Cannot sign in

**"Too many failed sign-ins for this username. Try again in … minutes."** After 5 wrong passwords for one username,
sign-in for that username from that address is locked for 15 minutes. Wait, or ask another admin to reset your
password (see [Lost password](#lost-password-or-authenticator)). "Too many sign-in attempts from your address" means
more than 20 attempts in a minute from your address; wait a minute. The limits are settings: see
[All settings](config.md).

**Nothing happens, or "Cross-origin request refused."** MediaMTX UI accepts sign-in and every change only from its
own address, exactly as set by `DOMAIN` in `.env`: the same scheme (`https://`), the same name, and the same port.
Opening the UI by the server's IP address, by another name, or on another port does not work. Open it by exactly
`https://` followed by your `DOMAIN` (or `http://DOMAIN:8080` for a local network install). If you changed
`HTTPS_PORT` in `.env`, browsers must still reach it on 443 through something that forwards it there.

**Behind your own reverse proxy?** The proxy must pass the `Host` header and set `X-Forwarded-For` and
`X-Forwarded-Proto`, and its address must be in `TRUSTED_PROXIES`. When this is wrong, the warnings
[`proxy_scheme` or `proxy_host`](#warnings-at-the-top-of-the-page) appear for a signed-in user. See
[Behind a reverse proxy](behind-a-proxy.md).

**Asked for a code you do not have?** With an authenticator app set up, sign-in asks for its code. Lost your phone?
Enter one of your recovery codes instead. See [Lost password or authenticator](#lost-password-or-authenticator).

## The encoder cannot connect

OBS, ffmpeg or a camera says it cannot connect, or connects and is dropped at once. Open the stream's page in the
UI first: it shows the exact address and key to use, says when it sees the encoder, and shows notes about what
MediaMTX refused.

**1. Is the port open?** Each protocol uses its own port, and the firewall in front of the server (your provider's,
or your router's port forwarding) must let it through:

| Protocol | Port |
|---|---|
| RTMP (OBS's default, most encoders) | 1935/tcp |
| RTSP (cameras) | 8554/tcp |
| SRT | 8890/udp |
| WebRTC / WHIP | 8189/udp, plus the UI's 443/tcp |

For the TCP ports you can test from another machine, for example:

```bash
nc -vz stream.example.com 1935
```

`succeeded` or `open` means the port is reachable. UDP ports (SRT, WebRTC) cannot be tested this way.

**2. Is the protocol switched on?** Only the protocols chosen during setup are served. An admin can change that under
**Configuration** → **Quick setup** → **Choose the protocols to serve** (see [Configuration](configuration.md)).

**3. Is the key right?** Copy the address and key again from the stream's page; do not type them. When a key is
wrong, the sidecar's log has a line like this:

```
"msg":"auth: denied","ip":"203.0.113.7","action":"publish","path":"live","protocol":"rtmp","reason":"unknown credential or wrong secret"
```

MediaMTX's log shows the same connection being closed, with something like
`authentication failed: server replied with code 401`. On the **Logs** page, choose **Sidecar** or **MediaMTX** and
search for `denied` or the encoder's address.

After 20 failed attempts within 5 minutes from one address, that address is refused for a while, even with the right
key (the log says `auth: throttling address after repeated failures`). An encoder that retries with a wrong key can
cause that. Fix the key, stop the encoder, wait 5 minutes, then start it again.

**4. Exposure control?** If [exposure control](exposure-control.md) is on, the stream ports are closed unless
something opens them. With the default rules, a stream's publishing ports open to the address of a browser that has
the stream's page open: if the encoder runs on another machine or network than that browser, its address is not let
in. Open the stream page from the encoder's machine, or have an admin open the port for the encoder's address on the
**Exposure** page.

**5. The encoder was refused for its tracks?** If the stream's page shows "Your encoder was refused", the note says
what MediaMTX expected (for example, a holding screen's clip fixes the video and audio formats). See
[Holding screens](holding-screens.md) and [Encoders](encoders.md).

## Video does not play, or only after a delay

**It plays, but several seconds behind.** The player in the UI and on watch links uses WebRTC, which is close to real
time. When WebRTC does not work, it falls back to HLS, which plays a few seconds behind, and says so on screen. The
usual reasons:

- **UDP port 8189 is blocked.** WebRTC's video travels over UDP 8189. Open it in your provider's firewall, or forward
  it on your router. The rest of the player goes through the UI's own HTTPS port.
- **The encoder sends B-frames.** Browsers cannot play H.264 with B-frames over WebRTC. The stream's page then
  shows "B-frames from your encoder", and the player falls back to HLS. In OBS, set B-frames to 0 ("Max B-frames"
  for NVENC). See [Encoders](encoders.md).
- **The encoder is far behind already.** RTMP and RTSP add a second or two; a long keyframe interval adds more. Set a
  1 second keyframe interval and use CBR. See [Watching](watching.md).

**Nothing plays at all.** Check that the stream is live (its page says **Live**). For a private stream, viewers
without an account need the playback key. If the stream is live and nothing plays, the browser's console and the
**Logs** page (MediaMTX) usually say why.

## Recordings stop

MediaMTX UI protects the disk from filling up. While the recordings disk has less than 20 GB free, the oldest
recordings are deleted (never a stream's newest one). Below 5 GB free, recording is switched off for every stream,
and the `recordings_guard` warning appears on every page.

To get recording back:

1. Make room on the disk: delete old recordings on the **Recordings** page, or remove other files from the disk.
2. As an admin, open **Recordings** and press **Switch recording back on**. This needs 20 GB free again.

On a small disk, such as a Raspberry Pi's SD card, 20 GB may be more than you have. Lower both limits as described in
[Recordings](recordings.md).

## A container keeps restarting

`docker compose ps` shows `Restarting`, or a container keeps going `Up` and down.

Read the log of the container that restarts:

```bash
docker compose logs --tail=50 sidecar
```

Common messages:

- **`mtxui: invalid settings, refusing to start:`** followed by a list. Every setting that is wrong is named. Fix them
  in `.env` or `compose.override.yaml` (see [All settings](config.md)) and run `docker compose up -d`.
- **`set DOMAIN in .env`** (from `docker compose` itself): `.env` is missing, or has no `DOMAIN=` line, or you are in
  the wrong directory.
- **`permission denied`** on a path you set with `RECORDINGS_PATH` or `BACKUPS_PATH`: the directory must belong to
  user 10002, the user the containers run as. Fix it (with your path), then `docker compose up -d`:

  ```bash
  sudo chown -R 10002:10002 /srv/recordings
  ```

- **`dependency failed to start: container mediamtx-ui-sidecar-1 is unhealthy`**: MediaMTX starts only once the
  sidecar is healthy. The problem is in the sidecar's log, not MediaMTX's.

**"The sidecar refuses to serve"** (as a red banner, or in the log as `startup safety check failed: refusing to
serve`): the sidecar found that MediaMTX answers its control API without a password. That would let anyone control
MediaMTX, so the UI stops serving until it is fixed (its health check keeps answering, so the container stays up).
It means MediaMTX is not running with the configuration the sidecar wrote. The sidecar puts its configuration back
when `mediamtx.yml` is changed into something invalid, so this usually clears by itself; if it does not, restart the
stack with `docker compose restart`, and if it stays, [report it](#reporting-a-problem) with both logs.

**"MediaMTX is not answering"** (a red banner): the sidecar cannot reach MediaMTX. The pages show the last known
state and catch up by themselves when it is back. Check `docker compose ps` and MediaMTX's log:

```bash
docker compose logs --tail=50 mediamtx
```

## Warnings at the top of the page

The sidecar checks itself and its surroundings, and shows what it finds at the top of every page for viewers,
operators and admins. Each warning has a code (shown here; the page shows only the message). The sidecar's log names
the code when a warning first appears.

| Code | What you see | What it means | What to do |
|---|---|---|---|
| `version` | "MediaMTX is version …, but this sidecar was built for …" | The two images do not match. Usually one was upgraded alone, or `MTXUI_VERSION` is set. | Download the release's `compose.yaml` and recreate the stack: see [Upgrading](upgrading.md). Remove `MTXUI_VERSION` from `.env` unless you need it. |
| `proxy_scheme` | "PUBLIC_URL is https, but requests arrive over plain http …" | Behind your own reverse proxy: the sidecar does not see that the browser used HTTPS, so the proxy does not send `X-Forwarded-Proto`, or its address is not trusted. Appears for 15 minutes after such a request. | Set `X-Forwarded-Proto` in the proxy, and its address in `TRUSTED_PROXIES`: see [Behind a reverse proxy](behind-a-proxy.md). |
| `proxy_host` | "Requests arrive for host …, but PUBLIC_URL is …" | Signed-in requests arrive for another name than `DOMAIN`: the proxy does not pass the `Host` header, or people open the UI by another name. Sign-in and changes only work from `DOMAIN`. Appears for 15 minutes after such a request. | Pass `Host` in the proxy; open the UI only by `DOMAIN`; if the name changed for good, see [FAQ](faq.md#can-i-change-the-domain-later). |
| `exposed_9997`, `exposed_9998`, `exposed_9996`, `exposed_8888`, `exposed_8889` | "Port … (MediaMTX …) accepts connections at …. It must never be published." | One of MediaMTX's internal ports (control API, metrics, playback, HLS, WebRTC signalling) answers at your public address. MediaMTX UI never publishes these. | Look for a `ports:` entry for MediaMTX in `compose.override.yaml` and remove it. Another program on the host (an older MediaMTX installed separately, for example) can also cause it: stop it. Then close the port in your firewall. |
| `config_drift` | "mediamtx.yml was changed outside the sidecar …" | Someone or something edited `mediamtx.yml` directly instead of in the UI. A valid change is recorded as a new version in the history; an invalid one is undone; a missing file is restored. | Admins see **Open the history** and **Dismiss**. Check the change in **Configuration** → **History**, then dismiss. Make changes in the UI to avoid it. |
| `config_check` | "The sidecar cannot check mediamtx.yml: …" | The sidecar could not read or check the configuration file. | Check the sidecar's log; the `data-config` volume must be in place. It clears by itself once the check works again. |
| `recordings_guard` | "Recording is switched off: only … GB were free …" | The recordings disk ran low and recording was switched off. | See [Recordings stop](#recordings-stop). |
| `recordings_budget` | "Recordings are … over the budget …" or "The recordings volume needs … more free space …" | The storage budget or free-space limit cannot be met by deleting recordings: the files are not recordings MediaMTX knows about (left from an earlier recording path, or copied there), or something else fills the disk. | Remove the files the message names by hand, or free space on the disk, or raise the budget. See [Recordings](recordings.md). |

## Lost password or authenticator

**Lost password, another admin exists:** the other admin opens **People**, presses **Reset password** for your
account, and gives you the join code it shows. Open `/join` on your UI's address, enter the code, and choose a new
password.

**Lost the authenticator app:** sign in with one of your recovery codes instead of the app's code ("Lost your phone?
Enter one of your recovery codes instead."). No recovery codes either: another admin presses **Reset second factor**
for your account on **People**.

**No other admin can sign in:** make the join code on the server. Replace `NAME` with the username:

```bash
docker compose exec sidecar /mtxui reset-password --username NAME
```

```
Join code for NAME: …
It works once, until 2026-10-04 12:00 UTC. Open the UI's /join page and enter it to choose a new password.
```

Open `https://` + your domain + `/join`, enter the code, and choose a new password. You are then signed in. The old
password keeps working until the code is used. A disabled account cannot be reset this way: the command says so.

**Locked out after too many tries?** The lock ends by itself after 15 minutes. Locks are kept in memory, so
restarting the sidecar (`docker compose restart sidecar`) also clears them.

Restoring a backup does not help with a lost password: it brings back the same accounts and passwords.

More about accounts, second factors and passkeys: [People and access](people.md).

## Reporting a problem

Bug reports are welcome on [GitHub issues](https://github.com/greatmastix/MediaMTXUI/issues). Please include:

1. **The version:**

   ```bash
   docker compose exec sidecar /mtxui version
   ```

2. **What you did, what you expected, and what happened instead.**
3. **Your setup:** the host (a VPS, a Raspberry Pi, …), the CPU type (`uname -m`), whether you use an override
   (reverse proxy, local network, exposure control), and the encoder or player involved.
4. **The logs** from around the time it happened. This saves both containers' logs into one file:

   ```bash
   docker compose logs --no-color --timestamps --tail=1000 > mediamtx-ui-logs.txt
   ```

   Admins can also use **Download** on the **Logs** page.

> [!WARNING]
> Read the logs before you post them publicly. The sidecar never writes passwords or stream keys into its log, but
> MediaMTX's log can contain parts of forward destinations (a key in a custom RTMP address, an SRT `streamid` or
> `passphrase`), and both logs contain your domain and visitors' IP addresses. Replace anything you do not want to
> share.

**Security problems** (a way to get in without permission, to see what you should not, to bypass a key): please do
**not** open a public issue. Report it privately as described in [SECURITY.md](../SECURITY.md).
