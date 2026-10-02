import { useQuery } from '@tanstack/react-query'
import { Link, Outlet } from '@tanstack/react-router'

import { sessionQuery } from '@/api/sidecar'
import { atLeast } from '@/lib/roles'

import { SavedNote } from './SavedNote'
import { SavedNoteContext, useSavedNote } from './useSavedNote'

const tabs = [
  { to: '/config/quick', label: 'Quick setup' },
  { to: '/config/paths', label: 'Paths' },
  { to: '/config/global', label: 'Global settings' },
  { to: '/config/path-defaults', label: 'Path defaults' },
  { to: '/config/yaml', label: 'YAML' },
  { to: '/config/history', label: 'History' },
] as const

/** Configuration: admins only. Every change is validated, written atomically and kept in the history. */
export function ConfigLayout() {
  const { data: session } = useQuery(sessionQuery)
  const saved = useSavedNote()
  if (!atLeast(session?.user.role ?? 'viewer', 'admin')) {
    return (
      <div className="space-y-2">
        <h1 className="font-heading text-2xl font-semibold">Configuration</h1>
        <p className="text-sm text-muted-foreground">The configuration is for admins.</p>
      </div>
    )
  }
  return (
    <div className="space-y-4">
      <div className="space-y-1">
        <h1 className="font-heading text-2xl font-semibold">Configuration</h1>
        <p className="text-sm text-muted-foreground">
          mediamtx.yml. Every change is checked by the sidecar and by MediaMTX before it is written,
          only the lines it touches change, and the history keeps every version.
        </p>
      </div>
      <nav
        aria-label="Configuration"
        className="inline-flex max-w-full flex-wrap gap-0.5 rounded-lg border bg-secondary p-0.5"
      >
        {tabs.map((t) => (
          <Link
            key={t.to}
            to={t.to}
            className="rounded-md px-3 py-1 text-[12.5px] text-muted-foreground hover:text-foreground aria-[current=page]:bg-accent aria-[current=page]:text-foreground aria-[current=page]:shadow-[inset_0_-2px_0_var(--signal)]"
          >
            {t.label}
          </Link>
        ))}
      </nav>
      <SavedNote message={saved.message} warn={saved.warn} />
      <SavedNoteContext value={saved}>
        <Outlet />
      </SavedNoteContext>
    </div>
  )
}
