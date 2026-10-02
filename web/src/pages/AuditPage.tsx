import { useInfiniteQuery, useQuery } from '@tanstack/react-query'
import { Download, ScrollText } from 'lucide-react'
import { useState } from 'react'

import { auditExportURL, fetchAudit, type AuditFilter } from '@/api/audit'
import { sessionQuery } from '@/api/sidecar'
import { fieldClass } from '@/components/config/SettingsForm'
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
import { atLeast } from '@/lib/roles'

// The audit log: who (or the system) changed what, when and from where. Filter, page back, export. The log is
// append-only; nothing here can change it.

const actionGroups = [
  ['', 'Everything'],
  ['auth.', 'Signing in and confirming'],
  ['account.', 'Own account changes'],
  ['user.', 'People'],
  ['credential', 'Credentials'],
  ['stream.', 'Streams'],
  ['config.', 'Configuration'],
  ['exposure.', 'Exposure'],
  ['recordings.', 'Recordings'],
] as const

const localToISO = (v: string) => (v ? new Date(v).toISOString() : undefined)

export function AuditPage() {
  const { data: session } = useQuery(sessionQuery)
  const admin = atLeast(session?.user.role ?? 'viewer', 'admin')
  const [draft, setDraft] = useState({ actor: '', action: '', target: '', from: '', to: '' })
  const [filter, setFilter] = useState<AuditFilter>({})
  const log = useInfiniteQuery({
    queryKey: ['audit', filter],
    queryFn: ({ pageParam }) => fetchAudit(filter, pageParam),
    initialPageParam: undefined as number | undefined,
    getNextPageParam: (last) => (last.length === 100 ? last.at(-1)?.id : undefined),
    enabled: admin,
  })
  const rows = log.data?.pages.flat() ?? []
  const set =
    (k: keyof typeof draft) => (e: React.ChangeEvent<HTMLInputElement | HTMLSelectElement>) => {
      setDraft({ ...draft, [k]: e.target.value })
    }
  if (!admin) {
    return (
      <div className="space-y-2">
        <h1 className="font-heading text-2xl font-semibold">Audit log</h1>
        <p className="text-sm text-muted-foreground">The audit log is for admins.</p>
      </div>
    )
  }
  return (
    <div className="space-y-4">
      <div className="space-y-1">
        <h1 className="flex items-center gap-2 font-heading text-2xl font-semibold">
          <ScrollText className="size-6 text-signal" aria-hidden /> Audit log
        </h1>
        <p className="text-sm text-muted-foreground">
          Every change, by whom (or by the server itself, as “system”), from where and when. It can
          only grow: nobody can edit or delete entries.
        </p>
      </div>
      <form
        className="grid gap-3 rounded-xl border bg-card p-4 sm:grid-cols-3 lg:grid-cols-6"
        aria-label="Filter"
        onSubmit={(e) => {
          e.preventDefault()
          setFilter({
            actor: draft.actor.trim(),
            action: draft.action,
            target: draft.target.trim(),
            from: localToISO(draft.from),
            to: localToISO(draft.to),
          })
        }}
      >
        <label className="space-y-1 text-sm">
          <span className="font-medium">Who</span>
          <input
            className={fieldClass}
            value={draft.actor}
            placeholder="admin, system…"
            onChange={set('actor')}
          />
        </label>
        <label className="space-y-1 text-sm">
          <span className="font-medium">What</span>
          <select className={fieldClass} value={draft.action} onChange={set('action')}>
            {actionGroups.map(([v, label]) => (
              <option key={v} value={v}>
                {label}
              </option>
            ))}
          </select>
        </label>
        <label className="space-y-1 text-sm">
          <span className="font-medium">On</span>
          <input
            className={fieldClass}
            value={draft.target}
            placeholder="a stream, a person…"
            onChange={set('target')}
          />
        </label>
        <label className="space-y-1 text-sm">
          <span className="font-medium">From</span>
          <input
            type="datetime-local"
            className={fieldClass}
            value={draft.from}
            onChange={set('from')}
          />
        </label>
        <label className="space-y-1 text-sm">
          <span className="font-medium">Until</span>
          <input
            type="datetime-local"
            className={fieldClass}
            value={draft.to}
            onChange={set('to')}
          />
        </label>
        <div className="flex items-end gap-2">
          <Button type="submit">Show</Button>
        </div>
      </form>
      <div className="flex flex-wrap gap-2">
        {(['csv', 'json'] as const).map((f) => (
          <a
            key={f}
            className={buttonVariants({ size: 'sm', variant: 'outline' })}
            href={auditExportURL(filter, f)}
            download
          >
            <Download /> Export {f.toUpperCase()}
          </a>
        ))}
      </div>
      {log.error ? (
        <Alert variant="destructive" role="alert">
          <AlertDescription>{log.error.message}</AlertDescription>
        </Alert>
      ) : !log.data ? (
        <Skeleton className="h-40 w-full" />
      ) : rows.length === 0 ? (
        <p className="rounded-lg border border-dashed p-8 text-center text-sm text-muted-foreground">
          Nothing matches.
        </p>
      ) : (
        <div className="rounded-lg border">
          <Table aria-label="Audit log">
            <TableHeader>
              <TableRow>
                <TableHead>When</TableHead>
                <TableHead>Who</TableHead>
                <TableHead>What</TableHead>
                <TableHead>On</TableHead>
                <TableHead className="hidden lg:table-cell">Details</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {rows.map((e) => (
                <TableRow key={e.id} data-testid="audit-row">
                  <TableCell className="whitespace-nowrap tabular-nums">
                    {new Date(e.at).toLocaleString()}
                  </TableCell>
                  <TableCell>
                    {e.actor}
                    {e.ip && <span className="block text-xs text-muted-foreground">{e.ip}</span>}
                  </TableCell>
                  <TableCell className="font-mono text-xs">{e.action}</TableCell>
                  <TableCell className="max-w-48 truncate">{e.target}</TableCell>
                  <TableCell className="hidden max-w-md lg:table-cell">
                    <code
                      className="block truncate text-xs text-muted-foreground"
                      title={JSON.stringify(e.details)}
                    >
                      {Object.keys(e.details).length ? JSON.stringify(e.details) : ''}
                    </code>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      )}
      {log.hasNextPage && (
        <Button
          variant="outline"
          disabled={log.isFetchingNextPage}
          onClick={() => {
            void log.fetchNextPage()
          }}
        >
          Load more
        </Button>
      )}
    </div>
  )
}
