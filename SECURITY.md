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

The latest release. Upgrading is `docker compose pull && docker compose up -d`.

## What the design promises

These are the properties a report would break; each is enforced by tests or static checks.

- MediaMTX never decides authentication itself: it asks the sidecar about every connection, and nothing anonymous
  is ever written into its configuration (its defaults are an open relay). The one anonymous access is reading a
  stream an admin made public.
- MediaMTX's API, metrics, playback server and pprof are never published; browsers reach what they need through the
  sidecar, behind its sign-in and per-role permissions.
- Every `mediamtx.yml` is validated by the sidecar's own rules and by MediaMTX before it is written, atomically.
- Containers run as uid 10002 on read-only filesystems with every capability dropped and no new privileges; the
  sidecar holds no host or cloud credentials and does not mount the Docker socket.
- Secrets are generated on the first start and stored in the state volume (mode 600); tokens and keys never travel in
  URLs; logs never contain passwords or stream keys.
- State-changing requests must come from the UI's own origin with a CSRF token; every change is in the audit log,
  which cannot be edited.
- Backups are encrypted (age, with a scrypt-wrapped key) and are checked before anything of them is used; a restore
  never takes a path from the archive.
