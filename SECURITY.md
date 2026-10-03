# Security

MediaMTX UI decides who may publish and watch on a streaming server that is usually on the internet, so security
reports are welcome and taken seriously.

## Reporting a vulnerability

Please do not open a public issue. Report it privately, either through GitHub's
[private vulnerability reporting](https://github.com/greatmastix/MediaMTXUI/security/advisories/new) or by e-mail to
greatmastix@gmail.com, with what you found, how to reproduce it, and the version (`docker compose exec sidecar
/mtxui version`). You will get an answer within a week; a fix for a confirmed issue is released as soon as it is ready,
and you are credited in its release notes unless you prefer not to be.

Vulnerabilities in MediaMTX itself belong to [its project](https://github.com/bluenviron/mediamtx/security).

## Supported versions

The latest release. To upgrade, download its `compose.yaml` and recreate the stack: see
[Upgrading](README.md#upgrading).

## What the design promises

These are the properties a report would break; each is enforced by tests or static checks.

- MediaMTX never decides authentication itself: it asks the sidecar about every connection, and nothing anonymous
  is ever written into its configuration (its defaults are an open relay). The one anonymous access is reading a
  public stream (never publishing, never its recordings). Streams are public when they are created; the stream's
  owner (a streamer), an operator or an admin can make one private, and public again.
- MediaMTX's API, metrics, playback server and pprof are never published; browsers reach what they need through the
  sidecar, behind its sign-in and per-role permissions.
- Every `mediamtx.yml` is validated by the sidecar's own rules and by MediaMTX before it is written, atomically.
- Containers run as uid 10002 on read-only filesystems with every capability dropped and no new privileges; the
  sidecar holds no host or cloud credentials and does not mount the Docker socket.
- Secrets are generated on the first start and stored in the state volume (mode 600). Tokens and keys never travel
  in the URLs of requests to the UI, since URLs end up in logs (a reverse proxy's access log, the sidecar's request
  log), with two exceptions that come with the protocols: MediaMTX's HLS session id (`?session=`, bound to the
  viewer's address), and the session URL a WHIP or WHEP client gets back (`/rtc-session/<id>`), which is all a
  request needs to change or end that session while it lasts.
- The sidecar's own log never contains passwords or stream keys (MediaMTX's log can: see below).
- State-changing requests to the UI and its API must come from the UI's own origin with a CSRF token; every change
  is in the audit log, which cannot be edited. The WHIP and WHEP endpoints for encoders and players (`/whip/`,
  `/whep/`, `/rtc-session/`) are exempt by design: they ignore the UI's cookies (MediaMTX decides with the client's
  own key, from its `Authorization` header, or with none for reading a public stream), and a page on another site
  can send them an SDP offer, a `PATCH` or a `DELETE` only after a CORS preflight, which they never grant.
- Backups are encrypted (age, with a scrypt-wrapped key) and are checked before anything of them is used; a restore
  never takes a path from the archive.

## Known limits

These are known and need no report.

- "Confirm it's you" before admin-level changes accepts the password, an authenticator code or a passkey, whichever
  the user picks, even when they have set up a second factor. It stops someone who has only the session cookie, not
  someone at the signed-in browser itself if that browser has the password saved.
- MediaMTX logs a forward's destination when the forward starts, without its user name, password and fragment but
  with the rest of the URL. A key in the path of a custom RTMP or RTSP address, or an SRT address's `streamid` and
  `passphrase` (SRT carries them in the URL), therefore end up in MediaMTX's log, which admins can read and download
  in the UI. The forward form puts RTMP keys in the fragment and WHIP keys in a header for that reason.
- Because the WHIP and WHEP endpoints send no CORS headers, a web player on another site cannot use them; watch
  links and the UI's own player work.
