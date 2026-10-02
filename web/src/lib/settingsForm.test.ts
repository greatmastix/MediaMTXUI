import { describe, expect, it } from 'vitest'

import type { Setting } from '@/api/config'

import { changes, fromText, isHook, toText } from './settingsForm'

const s = (key: string, type: Setting['type'], items?: Setting['items']): Setting => ({
  key,
  type,
  items,
  section: 'General',
  default: null,
  description: '',
})

describe('settings form', () => {
  it('converts values to text and back by type', () => {
    const cases: [Setting, unknown, string][] = [
      [s('rtsp', 'boolean'), true, 'yes'],
      [s('rtsp', 'boolean'), false, 'no'],
      [s('readTimeout', 'string'), '10s', '10s'],
      [s('writeQueueSize', 'integer'), 512, '512'],
      [s('rpiCameraBrightness', 'number'), 0.5, '0.5'],
      [s('rtspTransports', 'array', 'string'), ['udp', 'tcp'], 'udp, tcp'],
      [s('rtspUDPSourcePortRange', 'array', 'integer'), [10000, 65535], '10000, 65535'],
      [
        s('forward', 'array', 'object'),
        [{ dest: 'srt://x' }],
        '[\n  {\n    "dest": "srt://x"\n  }\n]',
      ],
    ]
    for (const [setting, value, text] of cases) {
      expect(toText(setting, value)).toBe(text)
      expect(fromText(setting, text)).toEqual({ ok: true, value })
    }
    expect(toText(s('x', 'string'), undefined)).toBe('')
    expect(fromText(s('x', 'integer'), '  ')).toEqual({ ok: true, value: undefined })
  })

  it('explains bad input', () => {
    expect(fromText(s('n', 'integer'), '1.5')).toEqual({
      ok: false,
      error: 'Enter a whole number.',
    })
    expect(fromText(s('n', 'number'), 'abc')).toEqual({ ok: false, error: 'Enter a number.' })
    expect(fromText(s('p', 'array', 'integer'), '1, x')).toEqual({
      ok: false,
      error: 'Enter a whole number.',
    })
    expect(fromText(s('f', 'array', 'object'), '{')).toEqual({
      ok: false,
      error: 'This is not valid JSON.',
    })
    expect(fromText(s('f', 'array', 'object'), '{}')).toEqual({
      ok: false,
      error: 'Enter a JSON list.',
    })
  })

  it('reports only what changed', () => {
    const settings = [
      s('a', 'string'),
      s('b', 'boolean'),
      s('c', 'integer'),
      s('d', 'array', 'string'),
    ]
    const current = { a: 'x', b: true, d: ['tcp'] }
    const texts = { a: '', b: 'yes', c: '7', d: 'tcp' }
    expect(changes(settings, current, texts)).toEqual({ set: { c: 7 }, remove: ['a'], errors: {} })
    expect(changes(settings, current, { c: 'seven' }).errors).toEqual({
      c: 'Enter a whole number.',
    })
    expect(isHook('runOnReady')).toBe(true)
    expect(isHook('record')).toBe(false)
  })
})
