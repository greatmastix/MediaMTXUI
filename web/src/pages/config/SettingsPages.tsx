import { patchGlobal, patchPathDefaults, describeWrite } from '@/api/config'
import { SettingsForm } from '@/components/config/SettingsForm'
import { globalImpact } from '@/lib/impact'
import { useLive } from '@/live/useLive'
import { Skeleton } from '@/components/ui/skeleton'

import { useConfigNote } from './useSavedNote'
import { useConfig } from './useConfig'

export function GlobalSettingsPage() {
  const live = useLive()
  const { config, catalog, global, refresh } = useConfig()
  const note = useConfigNote()
  if (!config || !catalog) return <Skeleton className="h-64 w-full" />
  return (
    <>
      <SettingsForm
        idPrefix="global"
        settings={catalog.global}
        current={global}
        inherited={(s) => s.default}
        locked={config.locked}
        impact={(keys) => globalImpact(keys, live.lists)}
        onSave={async (c) => {
          const res = await patchGlobal(c)
          note(describeWrite(res))
          await refresh()
        }}
      />
    </>
  )
}

export function PathDefaultsPage() {
  const { config, catalog, pathDefaults, refresh } = useConfig()
  const note = useConfigNote()
  if (!config || !catalog) return <Skeleton className="h-64 w-full" />
  return (
    <>
      <p className="text-sm text-muted-foreground">
        What every path gets unless it sets a value of its own.
      </p>
      <SettingsForm
        idPrefix="defaults"
        settings={catalog.path}
        current={pathDefaults}
        inherited={(s) => s.default}
        impact={() =>
          'Paths without a value of their own pick this up: their clients may have to reconnect.'
        }
        onSave={async (c) => {
          const res = await patchPathDefaults(c)
          note(describeWrite(res))
          await refresh()
        }}
      />
    </>
  )
}
