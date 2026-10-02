# Contributing

Thanks for helping. Bug reports with steps to reproduce, and pull requests with tests, are both welcome; for a
bigger change, open an issue first so we can agree on the approach.

## Setup

You need bash and Docker, nothing else: every toolchain (Go, Node, golangci-lint, Playwright, shellcheck) runs in a
container pinned by digest in `deploy/tools.env`.

```bash
./dev help
./dev up            # a dev stack on 127.0.0.1: the UI on :18080, Vite with hot reload on :18173
./dev down          # stop it; ./dev clean also removes its data
```

`./dev up` creates the dev admin (`admin` / `dev-admin-password`, from `deploy/dev/dev.env`) and a test-pattern
publisher on path `test`; `./dev up --wizard` leaves setup to the web wizard instead. On a remote machine, tunnel the
ports first: `ssh -L 18080:127.0.0.1:18080 -L 18173:127.0.0.1:18173 <host>`.

Before sending a change:

```bash
./dev ci            # lint, unit and integration tests, generated files, the image build
./dev e2e           # Playwright against a throwaway production-like stack (about 4 minutes)
```

## Layout

```
compose.yaml   the public install (the e2e and dev stacks build on it: deploy/compose.{test,e2e,dev}.yml)
sidecar/       the Go sidecar: cmd/mtxui, internal/ (app: HTTP; mtxconf: config writer and validator; mtxauth:
               MediaMTX's auth callback; live: event stream; store: SQLite; backup, logs, portgate, ...)
web/           the React + TypeScript UI, embedded into the sidecar at build time
e2e/           Playwright specs, run inside the e2e stack
spec/          MediaMTX's vendored OpenAPI spec (changed only by ./dev bump-mediamtx)
docs/          documentation; config.md and api-coverage.md are generated (./dev gen)
deploy/        the optional compose overrides, the dev/test/e2e overlays, the exposure helper's host files
```

## Rules the code keeps

A change that breaks one of these is wrong even when the tests pass.

1. **Hardened containers**: no Docker socket, no `privileged`, no host network, no added capabilities; uid 10002,
   read-only root filesystem, `no-new-privileges`, memory and pid limits. `scripts/compose-lint.mjs` and
   `scripts/compose-assert.sh` check it.
2. **Only stream ports are published**; MediaMTX's API, metrics, playback, pprof, HLS and WebRTC signalling stay on the
   stack network.
3. **No secrets in `.env`, compose files or the repository**: the sidecar generates them on the first start.
4. **`mediamtx.yml` is written only by `internal/mtxconf`, after validation** (the sidecar's rules, then
   `mediamtx --validate-conf` with the pinned binary), atomically, with a snapshot of every version. Edits are byte
   patches of the lines they change, never a decode/encode round trip.
5. **Every mutating endpoint is audited**, and has a test that says so.
6. **No tokens or credentials in URLs** (reverse proxies log them); never log MediaMTX's auth payloads unredacted.
7. **`X-Forwarded-*` only from trusted proxies**, through `internal/auth/clientip`.
8. **Outbound URLs and user-supplied names go through `internal/netguard` and the path-name validators**; no request
   input ever becomes a filesystem path.
9. **No open relay**: nothing anonymous goes into `mediamtx.yml`.
10. **The sidecar holds no host credentials**; anything privileged goes through a host-side helper with its own policy.
11. **Generated files are never edited by hand**: `./dev gen`.

## Conventions

- **Go**: stdlib first, chi, `context.Context` first, errors wrapped with `%w`, table-driven tests run with `-race`,
  golangci-lint with gofumpt. Integration tests run the real pinned MediaMTX binary. Migrations are forward-only goose
  files.
- **TypeScript**: strict, no `any`; server state in TanStack Query; the sidecar's answers validated with zod; live data
  from the event stream (`useLive()`), not polling; no inline scripts or styles (CSP); keyboard-accessible controls;
  the first page load within the bundle budget (checked by the image build).
- **e2e**: specs that leave the stack as they found it run in parallel; one that disturbs it (restarts MediaMTX,
  stops the publisher) goes into the serial chain in `e2e/playwright.config.ts`. No fixed sleeps: wait for the
  condition. Accessibility is checked with axe.
- **HTTP**: every route has an access level (a test fails otherwise); errors are `{"error": kind, "message":
  sentence}`; bodies are JSON; state-changing requests pass the origin and CSRF checks.
- **Commits**: small and imperative; say what changed in the security posture, if anything.

## Upgrading MediaMTX

```bash
./dev bump-mediamtx 1.22.0
```

It vendors the new OpenAPI spec and updates every pin. `./dev test` then fails until
`sidecar/internal/mtxapi/operations.yaml` covers every operation of the new spec. Read MediaMTX's release notes for
changed behaviour (config keys, hook names, log lines the sidecar reads), then run `./dev ci` and `./dev e2e`.

## Releases

Pushing a tag `v1.2.3` runs the checks and publishes the multi-arch image to
`ghcr.io/greatmastix/mediamtxui` as `1.2.3`, `1.2`, `1` and `latest` (`.github/workflows/release.yml`). Every push
to `main` publishes `edge`.

The README's screenshots come from `./dev screenshots`.
