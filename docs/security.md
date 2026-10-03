# Security

What MediaMTX UI defends against, how, and the test or check that shows it. Every item below was reviewed for the
first public release; the evidence is a Go test (in `sidecar/`, run by `./dev test`), an end-to-end test (`e2e/`,
`./dev e2e`) or a `./dev` command. To report a vulnerability, see [SECURITY.md](../SECURITY.md).

**The setting.** The UI faces the internet, and so do the stream ports you open. The people it guards against are
anyone on the internet, and signed-in people who try to go beyond their role (a streamer reaching another stream, a
viewer changing settings). An admin can change everything by design; the host and Docker are trusted.

## Checklist

### Sign-in and sessions

- [x] **Passwords** are hashed with argon2id; a tampered or weakened hash is refused, and a hash made with weaker
  parameters is upgraded at the next sign-in.
  *Evidence:* `TestHashAndVerify`, `TestRejectsTamperedHashes` (internal/auth).
- [x] **Guessing is slow and does not reveal accounts.** Sign-ins are rate-limited per client address; five failures
  lock a username for 15 minutes, unknown usernames lock the same way, and an unknown username costs the same time
  as a wrong password. The lock table and the hasher's queue are bounded, so a flood of attempts cannot grow memory
  or queue CPU work; past the bound, sign-in answers "busy".
  *Evidence:* `TestLoginRateLimit`, `TestLoginTimingIsEqualised` (internal/app), `TestLockout`,
  `TestLockoutAttempts`, `TestLockoutBound`, `TestHasherBusy` (internal/auth), `TestLockoutIsolation`
  (internal/app).
- [x] **Sessions** are server-side, random, and end after 12 hours idle or 7 days in all (`MTXUI_SESSION_*`); signing
  out, a password change, a reset by an admin or a disabled account ends them.
  *Evidence:* `TestSessions`, `TestLoginLogout`, `TestJoinResetEndsSessions`, `TestPeopleEditing`.
- [x] **Second factors.** Authenticator codes (RFC 6238, one use each, counted against the lock) and recovery codes;
  passkeys need user verification (a PIN or biometric, not just a touch), work only over HTTPS, and are capped per
  person. Adding either needs the password again.
  *Evidence:* `TestTOTPVectors`, `TestTOTPSignIn`, `TestRecoveryCodes`, `TestPasskeys`,
  `TestPasskeyNeedsUserVerification`, `TestPasskeysNeedHTTPS`, `TestPasskeyCapAtStore`, `TestAccount`.
- [x] **"Confirm it's you"** before admin-level changes: people, stream credentials, exposure control, the raw YAML
  editor, restoring a config snapshot, backups and restores, and switching the confirmation off. A confirmation
  lasts an hour. (Everyday settings are not on the list; see [By design](#by-design).)
  *Evidence:* `TestStepUp`, `TestStepUpRoutes` (the list itself is a test).
- [x] **First setup** needs a one-time token printed by `docker compose exec sidecar /mtxui setup-token`, and runs
  once, atomically. Invitations are one-time join codes.
  *Evidence:* `TestToken` (internal/setup), `TestSetupIsAtomicAndOnce`, `TestStreamsAndJoinCodes`, the e2e setup
  project.
- [x] **There is always an admin**: the last enabled admin cannot be demoted, disabled or deleted, even by two
  requests at once.
  *Evidence:* `TestChangeUserKeepsAnAdmin` (internal/store).

### Who may do what

- [x] **Every route has an access level** (public, streamer, viewer, operator, admin), and every non-public route
  refuses anonymous requests.
  *Evidence:* `TestRouteRegistry`, `TestRoles`, `TestRolesAndRouting`.
- [x] **Streamers see only their own streams**, in the API and the event stream; MediaMTX's API is for viewers and
  up.
  *Evidence:* `TestStreamsAndStreamers`, `TestScopedSubscription`, `TestEventStreamsPerUser`.
- [x] **MediaMTX asks the sidecar about every connection** (`authMethod: http`). Publishing needs the stream's key;
  watching needs a key, a guest key, a signed-in viewer's short-lived ticket, or a public stream (read only, never
  publish, never playback). The endpoint answers only MediaMTX.
  *Evidence:* `TestDecide`, `TestViewerTickets`, `TestGuestKeys`, `TestPublicStreams`, `TestInternalListener`.

### Requests from other sites

- [x] **CSRF.** Every state-changing request must come from the UI's own origin (`Sec-Fetch-Site`, then `Origin` or
  `Referer`) and carry the session's CSRF token; endpoints without a session demand a JSON body, which a form on
  another site cannot send.
  *Evidence:* `TestSameOrigin`, `TestValidCSRF` (internal/auth), `TestCSRFOnProxiedMutations`.
- [x] **The event stream and downloads** (recordings, backups, logs, the audit export) are GET requests that change
  nothing. The session cookie is `SameSite=Lax`, and every answer carries `Cross-Origin-Resource-Policy:
  same-origin` and no CORS headers, so another site can neither send the cookie with a background request nor read
  an answer. An event stream ends when its session does.
  *Evidence:* `TestSessionCookie`, `TestSecurityHeaders`, `TestEventStreamEndsWithTheSession`.
- [x] **Cookies and headers.** Over HTTPS the cookie is `__Host-` prefixed, `Secure`, `HttpOnly`; responses carry a
  strict Content-Security-Policy (no inline code, `frame-ancestors 'none'`), `X-Content-Type-Options`,
  `Referrer-Policy: same-origin`, `Cross-Origin-Opener-Policy`, a Permissions-Policy, and HSTS when the sidecar serves
  HTTPS itself. The e2e tests fail on any CSP violation.
  *Evidence:* `TestSessionCookie`, `TestSecurityHeaders`, `TestRunTLS`, every e2e test.

### Requests and proxies

- [x] **Client addresses** come from `X-Forwarded-For` only when the request comes from `MTXUI_TRUSTED_PROXIES`;
  rate limits and locks key on the real client.
  *Evidence:* `TestForwardedHostOnlyFromTrustedProxies`, `TestMiddleware`, `TestRateKey` (internal/auth/clientip).
- [x] **Request smuggling.** Ambiguous requests (two lengths, unknown or listed transfer codings, malformed or folded
  headers, two hosts) are refused; a chunked request ends its connection, so a proxy that framed it by
  `Content-Length` cannot slip a second request past it. Proxied requests to MediaMTX are rebuilt, not passed through.
  *Evidence:* `TestRequestFraming`, `TestUpstreamRequestIsRebuilt` (internal/proxy).
- [x] **SSRF.** Every address MediaMTX can be told to connect to (a path's `source`, a forward's destination) is
  checked: loopback, link-local (cloud metadata), the stack's own subnet and reserved ranges are refused everywhere;
  private LAN ranges are allowed for pulled sources (cameras live there) and refused for forwards. The MediaMTX
  API, HLS, WebRTC and playback proxies have fixed upstreams, and the API proxy allows only the operations of the
  vendored spec. There are no remote instances in this release.
  *Evidence:* `TestSources`, `TestForward`, `FuzzCheckURL` (internal/netguard), `TestTableCoversVendoredSpec`,
  `TestHLS`, `TestWHEP`, `TestExternal` (internal/liveproxy).
- [x] **Path traversal.** No request input reaches a filesystem call: recordings are deleted through MediaMTX's API,
  backups are opened through an `os.Root` confined to the backups directory, holding clips are named by the sidecar,
  path names are validated against MediaMTX's own rules.
  *Evidence:* `TestNoRequestInputInFilesystemCalls`, `FuzzRecordingPath` (internal/app), `TestValid`, `FuzzValid`
  (internal/pathname), `TestBackupRefused`.

### Limits (denial of service)

- [x] Request headers: 64 KiB, read within 10 s; request bodies must arrive within 30 s (uploads get longer); JSON
  bodies are size-capped and strict (unknown fields refused); MediaMTX's answers through the proxy: 32 MiB.
- [x] Bounded tables: sign-in locks (100,000 keys, swept), passkey ceremonies (10,000), viewer tickets and WebRTC
  sessions, live tails of the log viewer (32), event streams per person, guest keys (10 per stream), forwards (5),
  passkeys (10), saved layouts (50), multi-view tiles (9), config history (1,000 snapshots).
- [x] Expensive work is queued or capped: password hashing (at most 16 waiting), recording exports (concurrent
  exports and length), holding clips (64 MiB), backup uploads, the backup key's work factor.
- [x] Anonymous requests do not fill the audit log; audit fields are clipped.
  *Evidence:* `TestBodyDeadline`, `TestHasherBusy`, `TestLockoutBound`, `TestRecordingExportCap`,
  `TestEventStreamsPerUser`, `TestSnapshotsPruned`, `TestWorkFactorCap`, `TestAuditAnonymous`, `TestPasskeyCapAtStore`.

### MediaMTX's configuration

- [x] **Nothing invalid or open reaches MediaMTX.** `mediamtx.yml` is written only by the sidecar, after validation:
  its own rules (authentication stays on, no anonymous publish or read, no listener conflicts, internal servers off
  the stream ports), then `mediamtx --validate-conf` with the pinned binary; then an atomic rename. Edits change only
  the lines they touch. If MediaMTX stops answering after a write, the previous file comes back.
  *Evidence:* `TestPipelineRejectsS4Cases`, `TestRules`, `TestEditAndReplace`, `TestWriterRestoresWhenMediaMTXStopsAnswering`,
  `FuzzEdits`, `TestRandomEdits` (internal/yamledit).
- [x] **The sidecar refuses to serve while MediaMTX's API answers anonymously** (an open default config).
  *Evidence:* `TestSafeMediaMTX`, `TestAnonymousAPIIsUnsafe` (internal/probe), `TestUnsafeGate`.

### Secrets

- [x] **Generated, not configured.** Secrets are created on the first start in the state volume (mode 600); none goes
  into `.env` or the compose file. Stream credentials are checked against an HMAC under a key in the state volume,
  so a copy of the database alone does not allow testing guesses; the keys a stream's page shows again, the
  authenticator secrets and the forward destinations (which carry other platforms' keys) are sealed with AES-256-GCM,
  each bound to its row. A forward's key never comes back to the browser.
  *Evidence:* `TestLoadKeyCreatesOncePrivately`, `TestSeal`, `TestCheck` (internal/credentials), `TestStreamForwards`.
- [x] **Not in URLs, not in logs.** Tokens travel in cookies and headers, never in URLs (a proxy's access log records
  URLs), except where the protocol puts them there: MediaMTX's HLS session id and a WHIP or WHEP session's URL
  ([SECURITY.md](../SECURITY.md)); MediaMTX's answers lose the credentials they carry (forward destinations, RTMP query strings) in the API
  proxy and the event stream, and the audit log records no secret. After
  every e2e run, `./dev e2e` searches the stack's logs for every secret the run used.
  *Evidence:* `TestRedaction` (internal/proxy), `TestQueryRedacted` (internal/live), `check_logged_secrets` in `dev`.
- [x] **No secret in the repository.** gitleaks reads every commit.
  *Evidence:* `./dev audit` ("no leaks found").

### Audit, backups, exposure control

- [x] **Every change is audited** (who, from where, when, what), the log is append-only, and the real client address
  is recorded behind a proxy.
  *Evidence:* `TestAuditIsAppendOnly` (internal/store), `TestAuditRecordsRealClients`, `TestAuditAnonymous`; the
  tests of mutating endpoints check their audit entries.
- [x] **Backups** are encrypted (age, X25519, the key wrapped with scrypt under your passphrase); a damaged or
  hostile backup file is refused before anything is restored.
  *Evidence:* `TestBackupAndRestore`, `TestMaliciousPayloads`, `FuzzOpen`, `TestWorkFactorCap` (internal/backup).
- [x] **Exposure control** (optional) goes through a root helper that reads request files and applies only what a
  root-owned policy allows.
  *Evidence:* `TestPolicyViolationsAreRefused`, `TestRequestFileIsReadSafely`, `FuzzApply` (internal/portgate).

### Containers, dependencies, image

- [x] **Containers** run as uid 10002 on a read-only filesystem, with every capability dropped, `no-new-privileges`,
  memory and process limits, no Docker socket, no host network; MediaMTX's API, metrics, pprof, playback, HLS and
  WebRTC signalling ports are never published.
  *Evidence:* `scripts/compose-lint.mjs` (`./dev lint`), `scripts/compose-assert.sh hardening` and `ports-none`
  against the running e2e stack (`./dev e2e`).
- [x] **No known vulnerabilities.** govulncheck reads the sidecar's source (symbol level, with the toolchain and
  modules the image is built with) and MediaMTX's binary; npm audit allows no advisory for a dependency that reaches
  the browser bundle, and none critical for the build tools.
  *Evidence:* `./dev audit`. 2026-10-03: govulncheck found no vulnerability the code calls, and none in MediaMTX
  1.21.1; npm audit found none in shipped dependencies (one high advisory in a build tool, below).
- [x] **The image** is `FROM scratch`: the CA bundle, the sidecar's two binaries and MediaMTX's binary, nothing else
  to scan. It is built for amd64, arm64 and armv7 with provenance and an SBOM.
  *Evidence:* `Dockerfile`, `.github/workflows/release.yml`, `./dev audit`.
- [x] **Fuzzing** of the YAML editor, the validators and every parser of untrusted input: `FuzzEdits` (yamledit),
  `FuzzValid` (path names), `FuzzCheckURL` (netguard), `FuzzApply` (the exposure helper's request files), `FuzzOpen`
  (backup files), `FuzzRecordingPath`. Their seed corpora run with every `./dev test`.
  *Evidence:* `./dev fuzz [seconds]`.
- [x] **Upgrades** keep the data: the previous release's image creates people, streams, credentials, config history
  and audit entries, and this checkout's image opens them.
  *Evidence:* `./dev upgrade-test <ref>`.

## By design

Decisions, and limits we accept, so you can judge them for your setup.

- **New streams are public.** A public stream can be watched by anyone with its watch link: reading only, never
  publishing, never playback of recordings. The stream's owner or an operator can make it private on the stream's
  page; then viewers need a key, a guest key or a sign-in.
- **"Confirm it's you" covers admin-level actions only** (the list above). Everyday changes, such as the guided
  settings, a path's settings or stream settings, do not ask: each is validated before MediaMTX sees it and recorded
  in the audit log, and asking every time trains people to confirm without reading.
- **An open live view counts as activity.** A page that shows live data keeps its session from timing out while it
  is open (a wall display stays signed in); the maximum session age still ends it.
- **What MediaMTX does after the check is MediaMTX's.** A hostname that changes its address after it was checked
  (DNS rebinding), or an HTTP redirect or WHIP `Location` header that MediaMTX follows, can lead MediaMTX somewhere
  the check would refuse. Only admins set sources, stream owners set forwards, and everything inside
  the stack authenticates every request, so nothing there trusts a connection for its source address alone.
- **Plain HTTP** (a `PUBLIC_URL` with `http://`, for a LAN) has no `Secure` cookie and no passkeys. Use HTTPS on
  anything reachable from the internet.
- **Stream protocols carry their own risks.** RTMP, RTSP and SRT without encryption send stream keys in clear; use
  RTMPS, RTSPS or SRT with a passphrase where that matters. MediaMTX's HLS session IDs appear in URLs; they are bound
  to the viewer's address.
- **The known limits in [SECURITY.md](../SECURITY.md#known-limits)**: "confirm it's you" accepts the password even
  with a second factor set up; MediaMTX's log shows a forward's URL without its credentials and fragment, but with
  a key written into its path; the WHIP and WHEP endpoints send no CORS headers.
- **A build tool's advisory.** `braces` (GHSA-vfj7-8cjw-p6xm, high, no fixed version) is used by the shadcn
  command-line tool through `fast-glob`, at build time, on the project's own patterns; it never reaches the image or
  the browser.
