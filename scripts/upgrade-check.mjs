// upgrade-check.mjs BASE-URL PUBLIC-URL before|after: the API side of `./dev upgrade-test`. "before" (old image)
// makes data through the API; "after" (new image, same data) checks it is all still there and still works.
const [base, publicURL, phase] = process.argv.slice(2)
const origin = new URL(publicURL).origin
const password = 'upgrade-test-password-1'
let cookie = ''
let csrf = ''

async function call(method, path, body) {
  const headers = { Origin: origin, Host: new URL(publicURL).host }
  if (cookie) headers.Cookie = cookie
  if (csrf && method !== 'GET') headers['X-CSRF-Token'] = csrf
  if (body) headers['Content-Type'] = 'application/json'
  const res = await fetch(base + path, { method, headers, body: body ? JSON.stringify(body) : undefined, redirect: 'manual' })
  const set = res.headers.get('set-cookie')
  if (set) cookie = set.split(';')[0]
  const text = await res.text()
  let data
  try {
    data = JSON.parse(text)
  } catch {
    data = text
  }
  return { status: res.status, data }
}

function must(cond, what) {
  if (!cond) {
    console.error(`upgrade-check ${phase}: ${what}`)
    process.exit(1)
  }
  console.log(`ok  ${what}`)
}

for (let i = 0; i < 60; i++) {
  try {
    if ((await fetch(base + '/api/v1/setup')).ok) break
  } catch {
    // not up yet
  }
  await new Promise((r) => setTimeout(r, 1000))
}
const login = await call('POST', '/api/v1/auth/login', { username: 'admin', password })
must(login.status === 200, `the admin signs in (${login.status})`)
csrf = login.data.csrfToken

if (phase === 'before') {
  const st = await call('POST', '/api/v1/streams', { name: 'upgrade/test', title: 'Made before the upgrade' })
  must(st.status === 201, `a stream is created (${st.status})`)
  const inv = await call('POST', '/api/v1/users', { username: 'upgrade-viewer', role: 'viewer' })
  must(inv.status === 201, `a person is invited (${inv.status})`)
} else {
  const streams = await call('GET', '/api/v1/streams')
  must(streams.status === 200 && streams.data.some((s) => s.name === 'upgrade/test' && s.title === 'Made before the upgrade'), 'the stream is still there')
  const people = await call('GET', '/api/v1/users')
  must(people.status === 200 && people.data.some((p) => p.username === 'upgrade-viewer'), 'the invited person is still there')
  const creds = await call('GET', '/api/v1/credentials')
  must(creds.status === 200 && JSON.stringify(creds.data).includes('upgrade-cam'), 'the stream credential is still there')
  const conf = await call('GET', '/api/v1/config')
  must(conf.status === 200 && String(conf.data.content ?? conf.data).includes('upgrade/test'), "mediamtx.yml still has the stream's path")
  const snaps = await call('GET', '/api/v1/config/snapshots')
  must(snaps.status === 200 && snaps.data.length >= 2, `the config history is kept (${snaps.data.length} versions)`)
  const audit = await call('GET', '/api/v1/audit?action=stream.')
  must(audit.status === 200 && audit.data.length >= 1, 'the audit log is kept')
  const more = await call('POST', '/api/v1/streams', { name: 'upgrade/after', title: 'Made after' })
  must(more.status === 201, `the upgraded server takes changes (${more.status})`)
}
