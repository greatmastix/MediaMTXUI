import { Link, useNavigate } from '@tanstack/react-router'
import { Plus } from 'lucide-react'
import { useState } from 'react'

import { ApiError } from '@/api/client'
import { deletePath, describeWrite, savePath } from '@/api/config'
import { SettingsForm, fieldClass } from '@/components/config/SettingsForm'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button, buttonVariants } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { pathImpact } from '@/lib/impact'
import { cn } from '@/lib/utils'
import { useLive } from '@/live/useLive'

import { useConfigNote } from './useSavedNote'
import { useConfig, type Values } from './useConfig'

const str = (v: unknown) => (typeof v === 'string' ? v : undefined)

export function PathsConfigPage() {
  const { config, paths, pathDefaults, refresh } = useConfig()
  const [confirm, setConfirm] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const note = useConfigNote()
  if (!config) return <Skeleton className="h-40 w-full" />
  const names = Object.keys(paths).sort((a, b) => a.localeCompare(b))
  const remove = async (name: string) => {
    setError(null)
    try {
      await deletePath(name)
      note(`Path ${name} removed.`)
      setConfirm(null)
      await refresh()
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'The path could not be removed.')
    }
  }
  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <p className="text-sm text-muted-foreground">
          Paths configured in mediamtx.yml. A name starting with ~ is a regular expression;
          all_others catches every other name.
        </p>
        <Link to="/config/new-path" className={buttonVariants()}>
          <Plus aria-hidden />
          New path
        </Link>
      </div>
      {error && (
        <Alert variant="destructive" role="alert">
          <AlertDescription>{error}</AlertDescription>
        </Alert>
      )}
      {names.length === 0 ? (
        <p className="rounded-lg border border-dashed p-8 text-center text-sm text-muted-foreground">
          No paths configured: nothing can be published or read until one exists.
        </p>
      ) : (
        <div className="rounded-lg border">
          <Table aria-label="Configured paths">
            <TableHeader>
              <TableRow>
                <TableHead>Name</TableHead>
                <TableHead>Source</TableHead>
                <TableHead className="hidden md:table-cell">Record</TableHead>
                <TableHead className="text-right">
                  <span className="sr-only">Actions</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {names.map((name) => {
                const p = (paths[name] ?? {}) as Values
                const source = str(p.source) ?? str(pathDefaults.source) ?? 'publisher'
                const record = (p.record ?? pathDefaults.record) === true
                return (
                  <TableRow key={name} data-testid={`config-path-${name}`}>
                    <TableCell className="max-w-64 truncate font-mono">
                      <Link
                        to="/config/paths/$"
                        params={{ _splat: name }}
                        className="underline-offset-4 hover:underline"
                      >
                        {name}
                      </Link>
                    </TableCell>
                    <TableCell className="max-w-80 truncate font-mono text-xs">
                      {redact(source)}
                    </TableCell>
                    <TableCell className="hidden md:table-cell">{record ? 'yes' : 'no'}</TableCell>
                    <TableCell className="text-right">
                      {confirm === name ? (
                        <span className="inline-flex gap-2">
                          <Button
                            size="sm"
                            variant="destructive"
                            onClick={() => {
                              void remove(name)
                            }}
                          >
                            Remove {name}
                          </Button>
                          <Button
                            size="sm"
                            variant="outline"
                            onClick={() => {
                              setConfirm(null)
                            }}
                          >
                            Keep
                          </Button>
                        </span>
                      ) : (
                        <Button
                          size="sm"
                          variant="ghost"
                          onClick={() => {
                            setConfirm(name)
                          }}
                        >
                          Remove
                        </Button>
                      )}
                    </TableCell>
                  </TableRow>
                )
              })}
            </TableBody>
          </Table>
        </div>
      )}
    </div>
  )
}

/** Hides a password in a source URL for the list; the editor shows it. */
function redact(source: string) {
  return source.replace(/^([a-z0-9+.-]+:\/\/[^:/@]+:)[^@/]*@/i, '$1•••@')
}

const keywordSources = [
  {
    value: 'publisher',
    label: 'Publisher: clients publish to this path (RTSP, RTMP, SRT, WebRTC)',
  },
  {
    value: 'redirect',
    label: 'Redirect: readers are sent to another path or server (sourceRedirect)',
  },
  { value: 'rpiCamera', label: 'Raspberry Pi camera' },
] as const

const urlExamples = [
  'rtsp://user:pass@camera:554/stream',
  'rtsps://…, rtsp+http://…, rtsps+http://…, rtsp+ws://…, rtsps+ws://…',
  'rtmp://server/app#streamKey, rtmps://…',
  'http(s)://server/stream.m3u8 (HLS)',
  'srt://server:8890?streamid=read:path',
  'whep(s)://server/path/whep, moqt://…',
  'udp+mpegts://238.0.0.1:1234, udp+rtp://:5004 (MediaMTX listens)',
]

/** Creates or edits one path: its name, its source and every other path setting. */
export function PathEditorPage({ name: existing }: { name?: string }) {
  const { config, catalog, paths, pathDefaults, pathValues, refresh } = useConfig()
  const navigate = useNavigate()
  const note = useConfigNote()
  if (!config || !catalog) return <Skeleton className="h-64 w-full" />
  if (existing !== undefined && !(existing in paths)) {
    return (
      <div className="space-y-2">
        <p className="text-sm text-muted-foreground" data-testid="config-path-missing">
          There is no path {existing} in mediamtx.yml.
        </p>
        <Link to="/config/paths" className="text-sm underline underline-offset-4">
          Back to the paths
        </Link>
      </div>
    )
  }
  const current = existing === undefined ? {} : pathValues(existing)
  return (
    <PathForm
      key={`${existing ?? 'new'}-${config.sha256}`}
      existing={existing}
      current={current}
      catalog={catalog.path}
      pathDefaults={pathDefaults}
      taken={(n) => n in paths}
      onSaved={async (name, message) => {
        note(message)
        await refresh()
        if (existing === undefined)
          await navigate({ to: '/config/paths/$', params: { _splat: name } })
      }}
    />
  )
}

function PathForm({
  existing,
  current,
  catalog,
  pathDefaults,
  taken,
  onSaved,
}: {
  existing?: string
  current: Values
  catalog: Parameters<typeof SettingsForm>[0]['settings']
  pathDefaults: Values
  taken: (name: string) => boolean
  onSaved: (name: string, note: ReturnType<typeof describeWrite>) => Promise<void>
}) {
  const live = useLive()
  const [name, setName] = useState(existing ?? '')
  const initialSource = str(current.source) ?? ''
  const [source, setSource] = useState(initialSource)
  const inheritedSource = str(pathDefaults.source) ?? 'publisher'
  const [kind, setKind] = useState<string>(
    initialSource === ''
      ? ''
      : keywordSources.some((k) => k.value === initialSource)
        ? initialSource
        : 'url',
  )
  const extraDirty = (existing === undefined ? 1 : 0) + (source !== initialSource ? 1 : 0)

  return (
    <div className="space-y-4">
      <Link to="/config/paths" className="text-sm text-muted-foreground hover:text-foreground">
        ← Paths
      </Link>
      <h2 className="font-heading text-xl font-semibold break-all">{existing ?? 'New path'}</h2>
      <SettingsForm
        idPrefix="path"
        settings={catalog}
        current={current}
        inherited={(s) => pathDefaults[s.key] ?? s.default}
        exclude={new Set(['source'])}
        impact={() => (existing === undefined ? null : pathImpact(existing, live.lists))}
        extraDirty={extraDirty}
        saveLabel={existing === undefined ? 'Create path' : 'Save'}
        onSave={async ({ set, remove }) => {
          const target = existing ?? name.trim()
          if (kind === 'url' && !/^[a-z0-9+.-]+:\/\/./i.test(source)) {
            throw new ApiError(400, 'invalid', 'Enter the address to pull the stream from.')
          }
          if (!target) throw new ApiError(400, 'invalid', 'Give the path a name.')
          if (existing === undefined && taken(target)) {
            throw new ApiError(400, 'invalid', `There is already a path ${target}.`)
          }
          const dropped = new Set([...remove, 'source'])
          const next: Values = Object.fromEntries(
            Object.entries({ ...current, ...set }).filter(([k]) => !dropped.has(k)),
          )
          if (source !== '') next.source = source
          const res = await savePath(target, next)
          await onSaved(target, describeWrite(res))
        }}
      >
        <fieldset className="space-y-4 rounded-xl border bg-card p-4">
          <legend className="section-title px-1">Path</legend>
          {existing === undefined && (
            <div className="grid gap-2 md:grid-cols-[16rem_minmax(0,1fr)] md:gap-4">
              <label htmlFor="path-name" className="font-mono text-[13px]">
                name
              </label>
              <div className="space-y-1.5">
                <input
                  id="path-name"
                  className={cn(fieldClass, 'font-mono')}
                  spellCheck={false}
                  placeholder="cam1, live/front-door, ~^cams/(.+)$"
                  value={name}
                  onChange={(e) => {
                    setName(e.target.value)
                  }}
                />
                <p className="text-xs text-muted-foreground">
                  Letters, digits and _ . ~ - / ; a regular expression starts with ~.
                </p>
              </div>
            </div>
          )}
          <div className="grid gap-2 md:grid-cols-[16rem_minmax(0,1fr)] md:gap-4">
            <label htmlFor="path-source-kind" className="font-mono text-[13px]">
              source
            </label>
            <div className="space-y-2">
              <select
                id="path-source-kind"
                className={fieldClass}
                value={kind}
                onChange={(e) => {
                  const v = e.target.value
                  setKind(v)
                  setSource(v === 'url' ? (kind === 'url' ? source : 'rtsp://') : v)
                }}
              >
                <option value="">Default ({inheritedSource})</option>
                {keywordSources.map((k) => (
                  <option key={k.value} value={k.value}>
                    {k.label}
                  </option>
                ))}
                <option value="url">Pull from a URL (camera, server, stream)</option>
              </select>
              {kind === 'url' && (
                <input
                  aria-label="Source URL"
                  className={cn(fieldClass, 'font-mono')}
                  spellCheck={false}
                  value={source}
                  onChange={(e) => {
                    setSource(e.target.value)
                  }}
                />
              )}
              <p className="text-xs text-muted-foreground">
                {kind === 'url'
                  ? `MediaMTX connects to this address. Supported: ${urlExamples.join('; ')}. Addresses inside the stack, loopback and metadata services are refused.`
                  : 'Where the stream comes from.'}
              </p>
            </div>
          </div>
        </fieldset>
      </SettingsForm>
    </div>
  )
}
