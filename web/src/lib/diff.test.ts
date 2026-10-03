import { describe, expect, it } from 'vitest'

import { diffLines, hunks, maxCells, type DiffLine } from './diff'

describe('diff', () => {
  it('finds added, removed and unchanged lines', () => {
    const d = diffLines('a\nb\nc\n', 'a\nc\nd\n')
    expect(d.map((l) => `${l.kind} ${l.text}`)).toEqual(['same a', 'del b', 'same c', 'add d'])
    expect(d[3]).toMatchObject({ newNo: 3 })
  })

  it('groups changes with context', () => {
    const before = Array.from({ length: 20 }, (_, i) => `line ${i}`).join('\n')
    const after = before.replace('line 2', 'line two').replace('line 17', 'line seventeen')
    const h = hunks(diffLines(before, after), 2)
    expect(h).toHaveLength(2)
    expect(h[0]?.lines.map((l) => l.text)).toEqual([
      'line 0',
      'line 1',
      'line 2',
      'line two',
      'line 3',
      'line 4',
    ])
    expect(hunks(diffLines('same\n', 'same\n'))).toEqual([])
  })

  // Both versions read back from a diff: what it keeps and removes, and what it keeps and adds.
  const sides = (d: DiffLine[]) => [
    d.filter((l) => l.kind !== 'add').map((l) => l.text),
    d.filter((l) => l.kind !== 'del').map((l) => l.text),
  ]

  it('compares only what lies between the lines both versions start and end with', () => {
    const before = Array.from({ length: 3000 }, (_, i) => `line ${String(i)}`)
    const after = before.map((l) => (l === 'line 1500' ? 'line fifteen hundred' : l))
    const d = diffLines(before.join('\n'), after.join('\n'))
    expect(d.filter((l) => l.kind !== 'same')).toEqual([
      { kind: 'del', text: 'line 1500', oldNo: 1501 },
      { kind: 'add', text: 'line fifteen hundred', newNo: 1501 },
    ])
    expect(d.at(-1)).toEqual({ kind: 'same', text: 'line 2999', oldNo: 3000, newNo: 3000 })
    expect(sides(d)).toEqual([before, after])
  })

  it('stays within its memory budget for a file of any size', () => {
    // Every other line is shared, but the changed middle needs more table cells than allowed.
    const n = Math.ceil(Math.sqrt(maxCells) / 2) + 50
    const before = Array.from({ length: n }, (_, i) => [
      `old ${String(i)}`,
      `same ${String(i)}`,
    ]).flat()
    const after = Array.from({ length: n }, (_, i) => [
      `new ${String(i)}`,
      `same ${String(i)}`,
    ]).flat()
    const d = diffLines(before.join('\n'), after.join('\n'))
    expect(sides(d)).toEqual([before, after])
    // Shown as removed and added as a whole, apart from the last line, which both end with.
    expect(d.filter((l) => l.kind === 'same')).toEqual([
      { kind: 'same', text: `same ${String(n - 1)}`, oldNo: 2 * n, newNo: 2 * n },
    ])
    expect(d.findIndex((l) => l.kind === 'add')).toBe(2 * n - 1)
  })
})
