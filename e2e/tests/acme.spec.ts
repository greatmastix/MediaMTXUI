import { readFileSync } from 'node:fs'
import http from 'node:http'
import https from 'node:https'
import type { TLSSocket } from 'node:tls'

import { expect, test } from './fixtures'

// The public install's HTTPS (MTXUI_TLS=acme) against Let's Encrypt's test server, Pebble: the sidecar gets its
// certificate on its own and serves HTTPS with it, and its plain port only redirects (to PUBLIC_URL, never to the
// Host a request names).

const minica = readFileSync('/src/.cache/pebble/pebble.minica.pem')

interface Answer {
  status: number
  headers: http.IncomingHttpHeaders
  body: string
  issuer?: string
}

function get(url: string, ca?: string | Buffer, host?: string): Promise<Answer> {
  return new Promise((resolve, reject) => {
    const u = new URL(url)
    const mod = u.protocol === 'https:' ? https : http
    const req = mod.request(
      u,
      { ca, headers: host ? { Host: host } : {}, servername: u.hostname, timeout: 10_000 },
      (res) => {
        // The socket goes back to the pool once the body is read: take the certificate now.
        const sock = res.socket as TLSSocket | null
        const issuer =
          sock && typeof sock.getPeerCertificate === 'function'
            ? sock.getPeerCertificate().issuer.CN
            : undefined
        let body = ''
        res.on('data', (c: Buffer) => (body += c.toString()))
        res.on('end', () => {
          resolve({ status: res.statusCode ?? 0, headers: res.headers, body, issuer })
        })
      },
    )
    req.on('error', reject)
    req.end()
  })
}

test('the sidecar gets a certificate by ACME, serves HTTPS with it, and redirects plain HTTP', async () => {
  test.setTimeout(90_000)
  // Pebble makes up its root at start; its management API hands it out.
  const root = (await get('https://pebble:15000/roots/0', minica)).body
  expect(root).toContain('BEGIN CERTIFICATE')

  let answer: Answer | undefined
  await expect(async () => {
    answer = await get('https://acme.mtxe2e.test:9080/api/v1/setup', root)
    expect(answer.status).toBe(200)
  }).toPass({ timeout: 60_000, intervals: [1000] })
  expect(answer?.issuer).toMatch(/Pebble/)
  expect(answer?.headers['strict-transport-security']).toBe('max-age=31536000')

  // Not trusted without Pebble's root: a real certificate chain, not a self-signed fallback.
  await expect(get('https://acme.mtxe2e.test:9080/api/v1/setup')).rejects.toThrow()

  const plain = await get('http://acme.mtxe2e.test:9082/streams?x=1', undefined, 'evil.example')
  expect(plain.status).toBe(301)
  expect(plain.headers.location).toBe('https://acme.mtxe2e.test/streams?x=1')
})
