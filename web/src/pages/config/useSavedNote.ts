import { createContext, use, useCallback, useEffect, useState } from 'react'

/** A short-lived confirmation after a save. */
export function useSavedNote() {
  const [note, setNote] = useState<{ message: string; warn: boolean } | null>(null)
  useEffect(() => {
    if (!note || note.warn) return // warnings stay until the next save
    const t = setTimeout(() => {
      setNote(null)
    }, 6000)
    return () => {
      clearTimeout(t)
    }
  }, [note])
  const show = useCallback((message: string | { message: string; warn: boolean }) => {
    setNote(typeof message === 'string' ? { message, warn: false } : message)
  }, [])
  return { message: note?.message ?? null, warn: note?.warn ?? false, show }
}

export type SavedNoteState = ReturnType<typeof useSavedNote>

/** The configuration layout's note, which outlives the page that saved (a new path moves on to its own page). */
export const SavedNoteContext = createContext<SavedNoteState | null>(null)

export function useConfigNote(): SavedNoteState['show'] {
  const ctx = use(SavedNoteContext)
  if (!ctx) throw new Error('useConfigNote outside the configuration layout')
  return ctx.show
}
