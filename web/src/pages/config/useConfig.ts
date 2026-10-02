import { useQuery, useQueryClient } from '@tanstack/react-query'

import { catalogQuery, configQuery } from '@/api/config'

export type Values = Record<string, unknown>

const asValues = (v: unknown): Values =>
  v && typeof v === 'object' && !Array.isArray(v) ? (v as Values) : {}

/** The config, the settings catalog and the pieces the pages need. */
export function useConfig() {
  const queryClient = useQueryClient()
  const config = useQuery(configQuery)
  const catalog = useQuery(catalogQuery)
  const settings = config.data?.settings ?? {}
  return {
    config: config.data,
    catalog: catalog.data,
    error: config.error ?? catalog.error,
    global: Object.fromEntries(
      Object.entries(settings).filter(([k]) => k !== 'paths' && k !== 'pathDefaults'),
    ),
    pathDefaults: asValues(settings.pathDefaults),
    paths: asValues(settings.paths),
    pathValues: (name: string) => asValues(asValues(settings.paths)[name]),
    /** After a write: the config and its history are stale. */
    refresh: () => queryClient.invalidateQueries({ queryKey: ['config'] }),
  }
}
