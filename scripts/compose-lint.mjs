// Static checks of the production stack: `docker compose config --format json` of compose.yaml (with the optional
// overrides in deploy/) on stdin. Every service must run hardened (non-root, read-only root filesystem, no added
// capabilities, limits), publish only what it should (never MediaMTX's API, metrics, playback, pprof, HLS or WebRTC
// signalling) and get no secrets from the environment (the sidecar generates them). Some mounts must be read-only
// (below). Exits 1 with one line per problem.
const config = JSON.parse(await new Response(process.stdin).text())
const problems = []
const fail = (service, msg) => problems.push(`${service}: ${msg}`)

// Published ports each service may have: the UI for the sidecar, the stream ports for MediaMTX.
const allowedPorts = {
  sidecar: ['9080/tcp', '9082/tcp'], // the UI (HTTPS, or HTTP behind a proxy); ACME challenges and the redirect
  mediamtx: ['8554/tcp', '1935/tcp', '8890/udp', '8189/udp'],
}
const secretish = /secret|password|passwd|token|api[_-]?key|private/i

for (const [name, svc] of Object.entries(config.services ?? {})) {
  const user = String(svc.user ?? '')
  if (user === '' || /^(0|root)(:|$)/.test(user)) fail(name, `runs as root (user "${user}")`)
  if (svc.read_only !== true) fail(name, 'root filesystem is not read-only')
  if (svc.privileged) fail(name, 'is privileged')
  if (svc.network_mode === 'host') fail(name, 'uses the host network')
  if (!(svc.cap_drop ?? []).includes('ALL')) fail(name, 'does not drop all capabilities')
  if ((svc.cap_add ?? []).length > 0) fail(name, `adds capabilities ${svc.cap_add}`)
  if (!(svc.security_opt ?? []).some((o) => o.startsWith('no-new-privileges'))) fail(name, 'allows new privileges')
  if (!svc.mem_limit) fail(name, 'has no memory limit')
  if (!svc.pids_limit) fail(name, 'has no pids limit')
  for (const v of svc.volumes ?? []) {
    if (String(v.source ?? '').includes('docker.sock')) fail(name, 'mounts the Docker socket')
  }
  for (const p of svc.ports ?? []) {
    const port = `${p.target}/${p.protocol ?? 'tcp'}`
    if (!(allowedPorts[name] ?? []).includes(port)) fail(name, `publishes ${port}`)
  }
  for (const key of Object.keys(svc.environment ?? {})) {
    if (secretish.test(key)) fail(name, `gets ${key} from the environment; secrets are generated into state/`)
  }
}
for (const name of Object.keys(allowedPorts)) {
  if (!config.services?.[name]) fail(name, 'is missing')
}

// Mounts that must be read-only: the sidecar only measures recordings (deletion goes through MediaMTX's API);
// MediaMTX reads its config and the holding clips the sidecar writes.
for (const [name, target] of [
  ['sidecar', '/data/recordings'],
  ['mediamtx', '/config'],
  ['mediamtx', '/holding'],
]) {
  const v = (config.services?.[name]?.volumes ?? []).find((m) => m.target === target)
  if (!v) fail(name, `does not mount ${target}`)
  else if (v.read_only !== true) fail(name, `mounts ${target} writable; it must be read-only`)
}

if (problems.length > 0) {
  for (const p of problems) console.error(`compose-lint: ${p}`)
  process.exit(1)
}
console.log(`compose-lint: ${Object.keys(config.services).length} services ok`)
