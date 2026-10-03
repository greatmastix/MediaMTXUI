// A line diff for the config history: the longest common subsequence of lines, shown as unified hunks with a few
// lines of context. The lines both versions start and end with are matched first, so the quadratic table covers only
// what lies between them: small for an edit, however long the file.

export interface DiffLine {
  kind: 'same' | 'add' | 'del'
  text: string
  oldNo?: number
  newNo?: number
}

export interface Hunk {
  lines: DiffLine[]
}

/** The most table cells (4 bytes each) a diff may use. Beyond it, the changed middle is shown as removed and added as a
 * whole: still right, only not the shortest diff, and the page stays responsive for a file of any size. */
export const maxCells = 4_000_000

export function diffLines(a: string, b: string): DiffLine[] {
  const x = a.split('\n')
  const y = b.split('\n')
  if (x.at(-1) === '') x.pop()
  if (y.at(-1) === '') y.pop()
  let start = 0
  while (start < x.length && start < y.length && x[start] === y[start]) start++
  let endX = x.length
  let endY = y.length
  while (endX > start && endY > start && x[endX - 1] === y[endY - 1]) {
    endX--
    endY--
  }
  const out: DiffLine[] = []
  for (let k = 0; k < start; k++)
    out.push({ kind: 'same', text: x[k] ?? '', oldNo: k + 1, newNo: k + 1 })
  middle(x.slice(start, endX), y.slice(start, endY), start, out)
  for (let k = 0; endX + k < x.length; k++) {
    out.push({ kind: 'same', text: x[endX + k] ?? '', oldNo: endX + k + 1, newNo: endY + k + 1 })
  }
  return out
}

/** The diff of x and y, which start after `offset` lines of both versions, appended to out. */
function middle(x: string[], y: string[], offset: number, out: DiffLine[]) {
  const n = x.length
  const m = y.length
  const del = (i: number) => ({ kind: 'del' as const, text: x[i] ?? '', oldNo: offset + i + 1 })
  const add = (j: number) => ({ kind: 'add' as const, text: y[j] ?? '', newNo: offset + j + 1 })
  if ((n + 1) * (m + 1) > maxCells) {
    for (let i = 0; i < n; i++) out.push(del(i))
    for (let j = 0; j < m; j++) out.push(add(j))
    return
  }
  // lcs[i * (m + 1) + j]: the LCS length of x[i:] and y[j:]
  const lcs = new Uint32Array((n + 1) * (m + 1))
  for (let i = n - 1; i >= 0; i--) {
    for (let j = m - 1; j >= 0; j--) {
      lcs[i * (m + 1) + j] =
        x[i] === y[j]
          ? (lcs[(i + 1) * (m + 1) + j + 1] ?? 0) + 1
          : Math.max(lcs[(i + 1) * (m + 1) + j] ?? 0, lcs[i * (m + 1) + j + 1] ?? 0)
    }
  }
  let i = 0
  let j = 0
  while (i < n || j < m) {
    if (i < n && j < m && x[i] === y[j]) {
      out.push({ kind: 'same', text: x[i] ?? '', oldNo: offset + i + 1, newNo: offset + j + 1 })
      i++
      j++
    } else if (
      i < n &&
      (j >= m || (lcs[(i + 1) * (m + 1) + j] ?? 0) >= (lcs[i * (m + 1) + j + 1] ?? 0))
    ) {
      out.push(del(i)) // removals before additions, as in unified diffs
      i++
    } else {
      out.push(add(j))
      j++
    }
  }
}

/** Groups a diff into hunks: the changed lines with up to context unchanged lines around them. */
export function hunks(lines: readonly DiffLine[], context = 3): Hunk[] {
  const keep = new Array<boolean>(lines.length).fill(false)
  lines.forEach((l, idx) => {
    if (l.kind === 'same') return
    for (let k = Math.max(0, idx - context); k <= Math.min(lines.length - 1, idx + context); k++)
      keep[k] = true
  })
  const out: Hunk[] = []
  let start = -1
  for (let idx = 0; idx <= lines.length; idx++) {
    if (idx < lines.length && keep[idx]) {
      if (start < 0) start = idx
    } else if (start >= 0) {
      out.push({ lines: lines.slice(start, idx) })
      start = -1
    }
  }
  return out
}
