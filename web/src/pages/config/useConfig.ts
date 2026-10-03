import { useQuery, useQueryClient } from '@tanstack/react-query'

import { catalogQuery, configQuery } from '@/api/config'

export type Values = Record<string, unknown>

const asValues = (v: unknown): Values =>
  v && typeof v === 'object' && !Array.isArray(v) ? (v as Values) : {}

/** A path's own entry (own keys only: "toString" or "constructor" is a path name like any other). */
const entry = (paths: Values, name: string) =>
  Object.hasOwn(paths, name) ? asValues(paths[name]) : undefined

/** The config, the settings catalog and the pieces the pages need. */
export function useConfig() {
  const queryClient = useQueryClient()
  const config = useQuery(configQuery)
  const catalog = useQuery(catalogQuery)
  const settings = config.data?.settings ?? {}
  const paths = asValues(settings.paths)
  return {
    config: config.data,
    catalog: catalog.data,
    error: config.error ?? catalog.error,
    global: Object.fromEntries(
      Object.entries(settings).filter(([k]) => k !== 'paths' && k !== 'pathDefaults'),
    ),
    pathDefaults: asValues(settings.pathDefaults),
    paths,
    /** Whether mediamtx.yml has this path. */
    hasPath: (name: string) => Object.hasOwn(paths, name),
    pathValues: (name: string) => entry(paths, name) ?? {},
    /**
     * A path's settings as mediamtx.yml has them now, not as cached (undefined: no such path). A path is written whole,
     * so a write starts from these: what changed meanwhile elsewhere (a stream's recording, viewer limit, forwarding or
     * holding screen, the recordings guard) is kept, never written back from an older copy.
     */
    latestPath: async (name: string) =>
      entry(
        asValues((await queryClient.query({ ...configQuery, staleTime: 0 })).settings?.paths),
        name,
      ),
    /** After a write: the config and its history are stale. */
    refresh: () => queryClient.invalidateQueries({ queryKey: ['config'] }),
  }
}
