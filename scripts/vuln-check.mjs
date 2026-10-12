// vuln-check.mjs VEX [ID...]: reads govulncheck's OpenVEX report (-format openvex) and fails on every vulnerability it
// marks affected, except the IDs given, which ./dev audit tolerates for a pinned MediaMTX release (with why, in dev).
// A tolerated ID no longer affected is reported, so the exception goes once it is not needed.
import { readFileSync } from 'node:fs'

const [file, ...tolerated] = process.argv.slice(2)
const vex = JSON.parse(readFileSync(file, 'utf8'))
const affected = vex.statements.filter((s) => s.status === 'affected').map((s) => s.vulnerability.name)

let failed = false
for (const id of affected.sort()) {
  if (tolerated.includes(id)) {
    console.log(`tolerated  ${id}`)
  } else {
    console.log(`AFFECTED   ${id}  https://pkg.go.dev/vuln/${id}`)
    failed = true
  }
}
for (const id of tolerated.filter((t) => !affected.includes(t))) {
  console.log(`not needed ${id}: no longer affected, drop it from the tolerated list in dev`)
}
console.log(
  failed
    ? 'vulnerabilities that are not tolerated: see above'
    : `no vulnerabilities but the ${String(affected.length)} tolerated`,
)
process.exit(failed ? 1 : 0)
