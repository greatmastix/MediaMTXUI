// Measures how late the sidecar's live events arrive: it signs in, reads the event stream for a while and compares
// each history sample's timestamp (set by the sidecar when it takes the sample, every 5 s) with its arrival. A
// buffering reverse proxy shows up as seconds of delay. Run it on the same host as the sidecar, so both use one clock.
//
//   MTXUI_USER=admin MTXUI_PASSWORD=... node scripts/sse-latency.mjs https://mtx.example.com [seconds] [max-p95-ms]
//
// Exits 1 when the 95th percentile exceeds max-p95-ms (default 2500) or fewer than three samples arrive.

const [base, secondsArg = '30', maxArg = '2500'] = process.argv.slice(2)
const user = process.env.MTXUI_USER ?? 'admin'
const password = process.env.MTXUI_PASSWORD
if (!base || !password) {
  console.error('usage: MTXUI_USER=... MTXUI_PASSWORD=... node scripts/sse-latency.mjs BASE_URL [seconds] [max-p95-ms]')
  process.exit(2)
}
const origin = new URL(base).origin

const login = await fetch(`${origin}/api/v1/auth/login`, {
  method: 'POST',
  headers: { 'Content-Type': 'application/json', Origin: origin },
  body: JSON.stringify({ username: user, password }),
})
if (!login.ok) {
  console.error(`sse-latency: sign-in failed: ${login.status} ${await login.text()}`)
  process.exit(1)
}
const cookie = login.headers.getSetCookie().map((c) => c.split(';')[0]).join('; ')
const { csrfToken } = await login.json()

const opened = performance.now()
const ctl = new AbortController()
const res = await fetch(`${origin}/api/v1/events`, { headers: { Cookie: cookie, Accept: 'text/event-stream' }, signal: ctl.signal })
if (!res.ok || !res.body) {
  console.error(`sse-latency: the event stream answered ${res.status}`)
  process.exit(1)
}
setTimeout(() => ctl.abort(), Number(secondsArg) * 1000)

const delays = []
let firstEvent
let buf = ''
let event = ''
try {
  for await (const chunk of res.body.pipeThrough(new TextDecoderStream())) {
    buf += chunk
    let nl
    while ((nl = buf.indexOf('\n')) >= 0) {
      const line = buf.slice(0, nl)
      buf = buf.slice(nl + 1)
      if (line.startsWith('event: ')) {
        event = line.slice(7)
        firstEvent ??= performance.now() - opened
      } else if (line.startsWith('data: ') && event === 'sample') {
        delays.push(Date.now() - JSON.parse(line.slice(6)).t)
      } else if (line === '') {
        event = ''
      }
    }
  }
} catch (err) {
  if (err.name !== 'AbortError') throw err
}

await fetch(`${origin}/api/v1/auth/logout`, {
  method: 'POST',
  headers: { Cookie: cookie, Origin: origin, 'X-CSRF-Token': csrfToken },
})

delays.sort((a, b) => a - b)
const pct = (p) => delays[Math.min(delays.length - 1, Math.ceil((p / 100) * delays.length) - 1)]
const result = {
  samples: delays.length,
  firstEventMs: Math.round(firstEvent ?? -1),
  p50Ms: pct(50),
  p95Ms: pct(95),
  maxMs: delays.at(-1),
}
console.log(JSON.stringify(result))
if (delays.length < 3 || result.p95Ms > Number(maxArg)) {
  console.error(`sse-latency: failed (need at least 3 samples and p95 <= ${maxArg} ms)`)
  process.exit(1)
}
