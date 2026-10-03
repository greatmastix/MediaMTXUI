import { useState } from 'react'

import { ApiError } from '@/api/client'
import { replaceConfig, validateConfig, describeWrite } from '@/api/config'
import { fieldClass } from '@/components/config/SettingsForm'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { cn } from '@/lib/utils'

import { useConfigNote, type SavedNoteState } from './useSavedNote'
import { useConfig } from './useConfig'

/** The whole file as YAML, validated by the sidecar and MediaMTX before it is written. */
export function YamlPage() {
  const { config, refresh } = useConfig()
  const note = useConfigNote()
  if (!config) return <Skeleton className="h-96 w-full" />
  return (
    <YamlEditor content={config.content} sha256={config.sha256} refresh={refresh} note={note} />
  )
}

function YamlEditor({
  content,
  sha256,
  refresh,
  note,
}: {
  content: string
  sha256: string
  refresh: () => Promise<void>
  note: SavedNoteState['show']
}) {
  // The text being edited and the file it was made from. Unedited, the editor shows the file as it is now, also after
  // it changed elsewhere; edited, it keeps the edits and saves against the file they were made from, so a change made
  // meanwhile is reported (and refused on save), never discarded or overwritten unseen.
  const [draft, setDraft] = useState<{
    text: string
    from: { content: string; sha256: string }
  } | null>(null)
  const text = draft?.text ?? content
  const setText = (t: string) => {
    const from = draft?.from ?? { content, sha256 }
    setDraft(t === from.content ? null : { text: t, from })
  }
  const [reason, setReason] = useState('')
  const [problem, setProblem] = useState<{
    title: string
    message: string
    conflict?: boolean
  } | null>(null)
  const [valid, setValid] = useState<boolean | null>(null)
  const [busy, setBusy] = useState(false)
  const dirty = draft !== null
  const meanwhile = !busy && draft !== null && draft.from.sha256 !== sha256
  const reload = () => {
    setDraft(null)
    setProblem(null)
    void refresh()
  }

  const run = async (f: () => Promise<void>) => {
    setBusy(true)
    setProblem(null)
    try {
      await f()
    } catch (err) {
      if (err instanceof ApiError) {
        setProblem({
          title: err.kind === 'conflict' ? 'Changed meanwhile' : 'Not saved',
          message: err.message,
          conflict: err.kind === 'conflict',
        })
      } else {
        setProblem({ title: 'Not saved', message: 'The server could not be reached.' })
      }
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="space-y-3">
      <p className="text-sm text-muted-foreground">
        The file as MediaMTX reads it. Saving checks it first; hooks (runOn…) cannot be changed here
        yet.
      </p>
      {problem && (
        <Alert variant="destructive" role="alert">
          <AlertTitle>{problem.title}</AlertTitle>
          <AlertDescription className="whitespace-pre-wrap">
            {problem.message}
            {problem.conflict && (
              <Button variant="outline" size="sm" className="mt-2" onClick={reload}>
                Load the current file (your edits are discarded)
              </Button>
            )}
          </AlertDescription>
        </Alert>
      )}
      {meanwhile && !problem?.conflict && (
        <Alert role="status" data-testid="yaml-changed-meanwhile">
          <AlertTitle>Changed meanwhile</AlertTitle>
          <AlertDescription>
            mediamtx.yml was changed elsewhere after you started editing, so saving these edits is
            refused. Copy what you need, then load the current file.
            <Button variant="outline" size="sm" className="mt-2" onClick={reload}>
              Load the current file (your edits are discarded)
            </Button>
          </AlertDescription>
        </Alert>
      )}
      {valid && !problem && (
        <p role="status" className="text-sm" data-testid="yaml-valid">
          MediaMTX accepts this config.
        </p>
      )}
      <textarea
        aria-label="mediamtx.yml"
        className={cn(fieldClass, 'h-[60vh] min-h-80 resize-y font-mono text-xs leading-5')}
        spellCheck={false}
        value={text}
        onChange={(e) => {
          setText(e.target.value)
          setValid(null)
        }}
      />
      <div className="flex flex-wrap items-center gap-2">
        <input
          aria-label="Reason for the change"
          className={cn(fieldClass, 'max-w-sm')}
          placeholder="Reason (shown in the history)"
          value={reason}
          onChange={(e) => {
            setReason(e.target.value)
          }}
        />
        <div className="flex-1" />
        <span className="text-xs text-muted-foreground">{text.split('\n').length} lines</span>
        <Button
          variant="outline"
          disabled={busy}
          onClick={() => {
            void run(async () => {
              await validateConfig(text)
              setValid(true)
            })
          }}
        >
          Check
        </Button>
        <Button
          variant="outline"
          disabled={busy || !dirty}
          onClick={() => {
            setDraft(null)
            setProblem(null)
          }}
        >
          Discard
        </Button>
        <Button
          disabled={busy || !dirty}
          onClick={() => {
            void run(async () => {
              const res = await replaceConfig(text, draft?.from.sha256 ?? sha256, reason)
              note(describeWrite(res))
              await refresh()
              setDraft(null)
            })
          }}
        >
          Save
        </Button>
      </div>
    </div>
  )
}
