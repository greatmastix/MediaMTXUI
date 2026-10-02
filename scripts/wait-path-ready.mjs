// Usage: node wait-path-ready.mjs <sidecar-url> <public-url> <path> <user:password> [timeout-seconds]
// Signs in to the sidecar and polls MediaMTX's path through the sidecar's API proxy until it is ready; exits 1 on
// timeout. Requests carry PUBLIC_URL's host and origin, as a browser would, so the sidecar sees no proxy mismatch
// and accepts the sign-in. Runs in a throwaway container on the stack network (./dev check).
import http from 'node:http'

const [sidecar, publicURL, name, creds, timeout = '60'] = process.argv.slice(2)
if (!sidecar || !publicURL || !name || !creds?.includes(':')) {
  console.error('usage: wait-path-ready.mjs <sidecar-url> <public-url> <path> <user:password> [timeout-seconds]')
  process.exit(2)
}
const pub = new URL(publicURL)
const [username, ...rest] = creds.split(':')
const password = rest.join(':')

function request(method, path, { body, cookie, csrf } = {}) {
  return new Promise((resolve, reject) => {
    const headers = { Host: pub.host, Accept: 'application/json' }
    if (body) Object.assign(headers, { 'Content-Type': 'application/json', Origin: pub.origin })
    if (cookie) headers.Cookie = cookie
    if (csrf) headers['X-CSRF-Token'] = csrf
    const req = http.request(new URL(path, sidecar), { method, headers, timeout: 5000 }, (res) => {
      let data = ''
      res.on('data', (c) => (data += c))
      res.on('end', () => resolve({ status: res.statusCode, headers: res.headers, data }))
    })
    req.on('timeout', () => req.destroy(new Error('timeout')))
    req.on('error', reject)
    req.end(body ? JSON.stringify(body) : undefined)
  })
}

const deadline = Date.now() + Number(timeout) * 1000
let cookie = ''
let last = 'no response yet'
while (Date.now() < deadline) {
  try {
    if (!cookie) {
      const res = await request('POST', '/api/v1/auth/login', { body: { username, password } })
      if (res.status !== 200) throw new Error(`sign-in: HTTP ${res.status} ${res.data}`)
      cookie = (res.headers['set-cookie'] ?? []).map((c) => c.split(';')[0]).join('; ')
    }
    const res = await request('GET', `/api/mtx/v3/paths/get/${name}`, { cookie })
    if (res.status === 200) {
      const path = JSON.parse(res.data)
      if (path.ready) {
        console.log(`path "${name}" is ready: source ${path.source?.type ?? '?'}, tracks ${(path.tracks ?? []).join(', ') || 'none'}`)
        process.exit(0)
      }
      last = `not ready (source: ${path.source?.type ?? 'none'})`
    } else {
      last = `HTTP ${res.status}`
      if (res.status === 401) cookie = ''
    }
  } catch (err) {
    last = err.cause?.code ?? err.message
  }
  await new Promise((resolve) => setTimeout(resolve, 1000))
}
console.error(`path "${name}" not ready after ${timeout}s: ${last}`)
process.exit(1)
