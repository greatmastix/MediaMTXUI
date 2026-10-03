import { Lock, Search, TriangleAlert } from 'lucide-react'
import { useMemo, useState, type ReactNode } from 'react'

import { ApiError } from '@/api/client'
import type { Setting } from '@/api/config'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { changes, isHook, isJSON, toText, type Changes } from '@/lib/settingsForm'
import { cn } from '@/lib/utils'

export const fieldClass =
  'w-full min-w-0 rounded-lg border border-input bg-transparent px-2.5 py-1.5 text-sm outline-none focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50 disabled:cursor-not-allowed disabled:opacity-60 aria-invalid:border-destructive dark:bg-input/30'

interface Props {
  settings: readonly Setting[]
  /** This section's current values. */
  current: Readonly<Record<string, unknown>>
  /** What applies when a setting is not set here (MediaMTX's default, or for a path the path defaults). */
  inherited: (s: Setting) => unknown
  locked?: Readonly<Record<string, string>>
  /** Settings shown elsewhere on the page. */
  exclude?: ReadonlySet<string>
  idPrefix: string
  onSave: (c: Pick<Changes, 'set' | 'remove'>) => Promise<unknown>
  saveLabel?: string
  /** Extra fields above the settings (e.g. a path's name and source). */
  children?: ReactNode
  /** Extra changes from the children, counted and saved with the settings. */
  extraDirty?: number
  /** What saving the pending changes interrupts, shown in the save bar. */
  impact?: (changedKeys: string[]) => string | null
}

/** Every setting of a section, grouped as in MediaMTX's reference config, with a filter and a save bar. */
export function SettingsForm({
  settings,
  current,
  inherited,
  locked = {},
  exclude,
  idPrefix,
  onSave,
  saveLabel = 'Save',
  children,
  extraDirty = 0,
  impact,
}: Props) {
  const initial = useMemo(
    () => Object.fromEntries(settings.map((s) => [s.key, toText(s, current[s.key])])),
    [settings, current],
  )
  // Only what is typed here is kept; the other fields show the section as it is now, also when mediamtx.yml changes
  // elsewhere meanwhile (another admin, a stream's owner, the sidecar itself), and a save sends only what differs.
  const [edits, setEdits] = useState<Record<string, string>>({})
  const texts = { ...initial, ...edits }
  const [filter, setFilter] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)

  const pending = changes(settings, current, texts)
  const dirty =
    Object.keys(pending.set).length +
    pending.remove.length +
    Object.keys(pending.errors).length +
    extraDirty
  const warning =
    dirty > 0 && impact ? impact([...Object.keys(pending.set), ...pending.remove]) : null
  const q = filter.trim().toLowerCase()
  const shown = settings.filter(
    (s) =>
      !exclude?.has(s.key) &&
      (!q || s.key.toLowerCase().includes(q) || s.description.toLowerCase().includes(q)),
  )
  const sections = [...new Set(shown.map((s) => s.section))]

  const save = async () => {
    setError(null)
    if (Object.keys(pending.errors).length > 0) {
      setError('Fix the highlighted settings first.')
      return
    }
    setSaving(true)
    try {
      await onSave({ set: pending.set, remove: pending.remove })
      setEdits({})
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'The change could not be saved.')
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="space-y-6 pb-24">
      {children}
      <div className="relative max-w-sm">
        <Search
          className="absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground"
          aria-hidden
        />
        <Input
          type="search"
          aria-label="Filter settings"
          placeholder="Filter settings"
          className="pl-8"
          value={filter}
          onChange={(e) => {
            setFilter(e.target.value)
          }}
        />
      </div>
      {sections.length === 0 && (
        <p className="text-sm text-muted-foreground">No setting matches the filter.</p>
      )}
      {sections.map((section) => (
        <fieldset key={section} className="rounded-xl border bg-card p-4">
          <legend className="section-title px-1">{section}</legend>
          <div className="divide-y">
            {shown
              .filter((s) => s.section === section)
              .map((s) => (
                <Field
                  key={s.key}
                  setting={s}
                  id={`${idPrefix}-${s.key}`}
                  text={texts[s.key] ?? ''}
                  inherited={inherited(s)}
                  lockedWhy={locked[s.key] ?? (isHook(s.key) ? hookNote : undefined)}
                  changed={(texts[s.key] ?? '') !== initial[s.key]}
                  error={pending.errors[s.key]}
                  onChange={(v) => {
                    setEdits((t) => ({ ...t, [s.key]: v }))
                  }}
                />
              ))}
          </div>
        </fieldset>
      ))}
      <div
        className={cn(
          'fixed right-0 bottom-0 left-0 z-10 border-t bg-background/95 px-4 py-3 backdrop-blur md:left-60',
          dirty === 0 && !error && 'hidden',
        )}
      >
        <div className="flex flex-wrap items-center gap-3">
          {error && (
            <Alert variant="destructive" role="alert" className="w-full">
              <AlertDescription className="whitespace-pre-wrap">{error}</AlertDescription>
            </Alert>
          )}
          <span className="text-sm text-muted-foreground" data-testid="unsaved">
            {dirty === 1 ? '1 unsaved change' : `${dirty} unsaved changes`}
          </span>
          {warning && (
            <span className="flex items-center gap-1.5 text-sm" data-testid="impact">
              <TriangleAlert className="size-4 shrink-0 text-warning" aria-hidden />
              {warning}
            </span>
          )}
          <div className="flex-1" />
          <Button
            variant="outline"
            onClick={() => {
              setEdits({})
              setError(null)
            }}
            disabled={saving || dirty === 0}
          >
            Discard
          </Button>
          <Button
            onClick={() => {
              void save()
            }}
            disabled={saving || dirty === 0}
          >
            {saving ? 'Saving…' : saveLabel}
          </Button>
        </div>
      </div>
    </div>
  )
}

const hookNote =
  'Hooks run commands inside the MediaMTX container, so they get their own editor with re-authentication; until then they are read-only here.'

function describe(v: unknown): string {
  if (v === undefined || v === null || v === '') return 'empty'
  if (v === true) return 'yes'
  if (v === false) return 'no'
  if (Array.isArray(v)) return v.length === 0 ? 'none' : JSON.stringify(v)
  if (typeof v === 'object') return JSON.stringify(v)
  return typeof v === 'string' ? v : JSON.stringify(v)
}

function Field({
  setting: s,
  id,
  text,
  inherited,
  lockedWhy,
  changed,
  error,
  onChange,
}: {
  setting: Setting
  id: string
  text: string
  inherited: unknown
  lockedWhy?: string
  changed: boolean
  error?: string
  onChange: (v: string) => void
}) {
  const descId = `${id}-desc`
  const common = {
    id,
    disabled: !!lockedWhy,
    'aria-describedby': descId,
    'aria-invalid': error ? true : undefined,
  } as const
  let input: ReactNode
  if (s.type === 'boolean') {
    input = (
      <select
        {...common}
        className={cn(fieldClass, 'max-w-48')}
        value={text}
        onChange={(e) => {
          onChange(e.target.value)
        }}
      >
        <option value="">Default ({describe(inherited)})</option>
        <option value="yes">yes</option>
        <option value="no">no</option>
      </select>
    )
  } else if (isJSON(s)) {
    input = (
      <textarea
        {...common}
        className={cn(fieldClass, 'min-h-20 font-mono text-xs')}
        spellCheck={false}
        rows={Math.min(8, Math.max(3, text.split('\n').length))}
        placeholder={describe(inherited)}
        value={text}
        onChange={(e) => {
          onChange(e.target.value)
        }}
      />
    )
  } else {
    input = (
      <input
        {...common}
        className={cn(fieldClass, 'font-mono')}
        spellCheck={false}
        placeholder={describe(inherited)}
        inputMode={s.type === 'integer' || s.type === 'number' ? 'decimal' : undefined}
        value={text}
        onChange={(e) => {
          onChange(e.target.value)
        }}
      />
    )
  }
  return (
    <div className="grid gap-2 py-3 md:grid-cols-[16rem_minmax(0,1fr)] md:gap-4">
      <div className="min-w-0">
        <label htmlFor={id} className="flex items-center gap-1.5 font-mono text-[13px] break-all">
          {s.key}
          {lockedWhy && (
            <Lock className="size-3 shrink-0 text-muted-foreground" aria-label="read-only" />
          )}
          {changed && (
            <span className="size-1.5 shrink-0 rounded-full bg-signal" aria-label="changed" />
          )}
        </label>
      </div>
      <div className="min-w-0 space-y-1.5">
        {input}
        <p id={descId} className="text-xs whitespace-pre-line text-muted-foreground">
          {error ? <span className="text-destructive">{error} </span> : null}
          {lockedWhy ?? s.description}
          {!lockedWhy && s.type === 'array' && !isJSON(s) ? ' (comma-separated)' : ''}
        </p>
      </div>
    </div>
  )
}
