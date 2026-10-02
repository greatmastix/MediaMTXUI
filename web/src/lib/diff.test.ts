import { describe, expect, it } from 'vitest'

import { diffLines, hunks } from './diff'

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
})
