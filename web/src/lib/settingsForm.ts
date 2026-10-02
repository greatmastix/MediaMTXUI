import type { Setting } from '@/api/config'

// The settings forms edit text; these convert between a setting's value and its text by the setting's type, and turn
// the edited texts into a patch of what changed. An empty field means "not set": MediaMTX's default (or, for a path,
// the path defaults) applies.

const scalarItems = new Set(['string', 'integer', 'number', 'boolean'])

/** Whether a setting is edited as JSON (objects and lists of objects). */
export const isJSON = (s: Setting) =>
  s.type === 'object' || (s.type === 'array' && !scalarItems.has(s.items ?? 'object'))

/** A value as form text. */
export function toText(s: Setting, value: unknown): string {
  if (value === undefined || value === null) return ''
  if (value === true) return 'yes'
  if (value === false) return 'no'
  if (isJSON(s)) return JSON.stringify(value, null, 2)
  if (Array.isArray(value)) return value.map((v) => String(v)).join(', ')
  if (typeof value === 'object') return JSON.stringify(value)
  return typeof value === 'string' ? value : JSON.stringify(value)
}

export type Parsed = { ok: true; value: unknown } | { ok: false; error: string }

function scalar(type: string, text: string): Parsed {
  switch (type) {
    case 'integer':
      return /^-?\d+$/.test(text)
        ? { ok: true, value: Number(text) }
        : { ok: false, error: 'Enter a whole number.' }
    case 'number': {
      const n = Number(text)
      return text !== '' && Number.isFinite(n)
        ? { ok: true, value: n }
        : { ok: false, error: 'Enter a number.' }
    }
    case 'boolean':
      if (text === 'yes' || text === 'true') return { ok: true, value: true }
      if (text === 'no' || text === 'false') return { ok: true, value: false }
      return { ok: false, error: 'Choose yes or no.' }
    default:
      return { ok: true, value: text }
  }
}

/** Form text as a value; undefined means not set. */
export function fromText(s: Setting, text: string): Parsed {
  const t = s.type === 'string' ? text : text.trim()
  if (t === '') return { ok: true, value: undefined }
  if (isJSON(s)) {
    try {
      const v: unknown = JSON.parse(t)
      const want =
        s.type === 'array' ? Array.isArray(v) : typeof v === 'object' && !Array.isArray(v)
      return want
        ? { ok: true, value: v }
        : { ok: false, error: s.type === 'array' ? 'Enter a JSON list.' : 'Enter a JSON object.' }
    } catch {
      return { ok: false, error: 'This is not valid JSON.' }
    }
  }
  if (s.type === 'array') {
    const out: unknown[] = []
    for (const part of t.split(',').map((p) => p.trim())) {
      if (part === '') continue
      const p = scalar(s.items ?? 'string', part)
      if (!p.ok) return p
      out.push(p.value)
    }
    return { ok: true, value: out }
  }
  return scalar(s.type, t)
}

export interface Changes {
  set: Record<string, unknown>
  remove: string[]
  errors: Record<string, string>
}

/** What the edited texts change against the current values. */
export function changes(
  settings: readonly Setting[],
  current: Readonly<Record<string, unknown>>,
  texts: Readonly<Record<string, string>>,
): Changes {
  const out: Changes = { set: {}, remove: [], errors: {} }
  for (const s of settings) {
    const text = texts[s.key]
    if (text === undefined || text === toText(s, current[s.key])) continue
    const p = fromText(s, text)
    if (!p.ok) {
      out.errors[s.key] = p.error
      continue
    }
    if (p.value === undefined) {
      if (current[s.key] !== undefined) out.remove.push(s.key)
    } else if (JSON.stringify(p.value) !== JSON.stringify(current[s.key])) {
      out.set[s.key] = p.value
    }
  }
  return out
}

/** Hooks run commands in the MediaMTX container; they get their own editor (with step-up) later. */
export const isHook = (key: string) => key.startsWith('runOn')
