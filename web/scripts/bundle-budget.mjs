// Fails the build when the first page load needs more than the budget: every script and stylesheet that
// dist/index.html references (entry, module preloads, CSS), measured gzip-compressed, as browsers download them.
// Run by the image build after `npm run build`: node scripts/bundle-budget.mjs [dist]
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { gzipSync } from 'node:zlib'

const budget = { js: 250 * 1024, css: 20 * 1024 } // gzip bytes

const dist = process.argv[2] ?? 'dist'
const html = readFileSync(join(dist, 'index.html'), 'utf8')
const refs = [...html.matchAll(/(?:src|href)="\/(assets\/[^"]+\.(js|css))"/g)]
if (refs.length === 0) {
  console.error('bundle-budget: index.html references no assets')
  process.exit(1)
}
const total = { js: 0, css: 0 }
for (const [, file, kind] of refs) {
  const size = gzipSync(readFileSync(join(dist, file)), { level: 9 }).length
  total[kind] += size
  console.log(`bundle-budget: ${file} ${(size / 1024).toFixed(1)} KiB gzip`)
}
let ok = true
for (const kind of ['js', 'css']) {
  const line = `${kind}: ${(total[kind] / 1024).toFixed(1)} KiB of ${(budget[kind] / 1024).toFixed(0)} KiB`
  if (total[kind] > budget[kind]) {
    console.error(`bundle-budget: over budget, ${line}`)
    ok = false
  } else {
    console.log(`bundle-budget: ${line}`)
  }
}
process.exit(ok ? 0 : 1)
