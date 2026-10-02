import { useQuery } from '@tanstack/react-query'
import { Link, useNavigate } from '@tanstack/react-router'

import { sessionQuery } from '@/api/sidecar'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { Skeleton } from '@/components/ui/skeleton'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { useNow } from '@/hooks/useNow'
import { formatCount } from '@/lib/format'
import { protocols, type Item, type ListSpec, type Protocol } from '@/lib/protocols'
import { atLeast } from '@/lib/roles'
import { cn } from '@/lib/utils'
import { useLive } from '@/live/useLive'
import { keyField, listItems, type ListKind, type Lists } from '@/live/store'

export interface Selection {
  kind?: ListKind
  id?: string
}

function count(lists: Lists, p: Protocol): number | undefined {
  let n: number | undefined
  for (const l of p.lists) {
    const list = lists[l.kind]
    if (list?.available) n = (n ?? 0) + list.items.size
  }
  return n
}

export function ConnectionsPage({
  protocol,
  selection,
}: {
  protocol: Protocol
  selection: Selection
}) {
  const live = useLive()
  const { data: session } = useQuery(sessionQuery)
  if (!atLeast(session?.user.role ?? 'viewer', 'operator')) {
    return (
      <div className="space-y-2">
        <h1 className="font-heading text-2xl font-semibold">Connections</h1>
        <p className="text-sm text-muted-foreground">
          Connections and sessions are visible to operators and admins.
        </p>
      </div>
    )
  }
  return (
    <div className="space-y-4">
      <h1 className="font-heading text-2xl font-semibold">Connections</h1>
      <nav
        aria-label="Protocols"
        className="inline-flex max-w-full flex-wrap gap-0.5 rounded-lg border bg-secondary p-0.5"
      >
        {protocols.map((p) => {
          const n = count(live.lists, p)
          return (
            <Link
              key={p.id}
              to="/connections/$protocol"
              params={{ protocol: p.id }}
              className="flex items-center gap-1.5 rounded-md px-3 py-1 text-[12.5px] text-muted-foreground hover:text-foreground aria-[current=page]:bg-accent aria-[current=page]:text-foreground aria-[current=page]:shadow-[inset_0_-2px_0_var(--signal)]"
            >
              {p.label}{' '}
              <span
                className={cn(
                  'rounded-full px-1.5 text-xs tabular-nums',
                  n ? 'bg-background font-medium text-foreground' : 'text-muted-foreground',
                )}
              >
                {n === undefined ? 'off' : formatCount(n)}
              </span>
            </Link>
          )
        })}
      </nav>
      {protocol.lists.map((spec) => (
        <ListCard key={spec.kind} protocol={protocol} spec={spec} lists={live.lists} />
      ))}
      <Detail protocol={protocol} selection={selection} lists={live.lists} />
    </div>
  )
}

function ListCard({ protocol, spec, lists }: { protocol: Protocol; spec: ListSpec; lists: Lists }) {
  const now = useNow()
  const list = lists[spec.kind]
  const items: Item[] | undefined = listItems(lists, spec.kind)
  const key = keyField[spec.kind]
  return (
    <Card>
      <CardHeader>
        <CardTitle>
          <h2 className="section-title">
            {protocol.label} {spec.title.toLowerCase()}
            {list?.available && ` (${formatCount(list.items.size)})`}
          </h2>
        </CardTitle>
      </CardHeader>
      <CardContent>
        {!list || !items ? (
          <Skeleton className="h-16 w-full" />
        ) : !list.available ? (
          <p className="text-sm text-muted-foreground" data-testid={`off-${spec.kind}`}>
            {protocol.label} is switched off in MediaMTX&apos;s configuration.
          </p>
        ) : items.length === 0 ? (
          <p className="text-sm text-muted-foreground">None right now.</p>
        ) : (
          <Table aria-label={`${protocol.label} ${spec.title.toLowerCase()}`}>
            <TableHeader>
              <TableRow>
                {spec.columns.map((c) => (
                  <TableHead key={c.label} className={cn(c.numeric && 'text-right')}>
                    {c.label}
                  </TableHead>
                ))}
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.map((it) => {
                const id = String(it[key])
                return (
                  <TableRow key={id}>
                    {spec.columns.map((c, i) => (
                      <TableCell
                        key={c.label}
                        className={cn(c.numeric && 'text-right tabular-nums')}
                      >
                        {i === 0 ? (
                          <Link
                            to="/connections/$protocol"
                            params={{ protocol: protocol.id }}
                            search={{ kind: spec.kind, id }}
                            className="font-mono text-xs underline underline-offset-4"
                            aria-label={`Details of ${spec.title.toLowerCase().replace(/s$/, '')} ${id}`}
                          >
                            {c.cell(it, now)}
                          </Link>
                        ) : (
                          c.cell(it, now)
                        )}
                      </TableCell>
                    ))}
                  </TableRow>
                )
              })}
            </TableBody>
          </Table>
        )}
      </CardContent>
    </Card>
  )
}

function Detail({
  protocol,
  selection,
  lists,
}: {
  protocol: Protocol
  selection: Selection
  lists: Lists
}) {
  const navigate = useNavigate()
  const { kind, id } = selection
  const spec = protocol.lists.find((l) => l.kind === kind)
  const item = spec && id ? (lists[spec.kind]?.items.get(id) as Item | undefined) : undefined
  const close = () => {
    void navigate({ to: '/connections/$protocol', params: { protocol: protocol.id }, search: {} })
  }
  return (
    <Sheet
      open={!!spec && !!id}
      onOpenChange={(open) => {
        if (!open) close()
      }}
    >
      <SheetContent className="w-full overflow-y-auto data-[side=right]:w-full data-[side=right]:sm:max-w-xl">
        <SheetHeader>
          <SheetTitle>
            {protocol.label} {spec?.title.toLowerCase().replace(/s$/, '')}
          </SheetTitle>
          <SheetDescription className="font-mono text-xs break-all">{id}</SheetDescription>
        </SheetHeader>
        <div className="px-4 pb-4">
          {item ? (
            <dl
              className="grid grid-cols-[minmax(0,14rem)_1fr] gap-x-4 gap-y-1.5 text-sm"
              data-testid="connection-detail"
            >
              {Object.entries(item)
                .sort(([a], [b]) => a.localeCompare(b))
                .map(([k, v]) => (
                  <div key={k} className="contents">
                    <dt className="text-muted-foreground">{k}</dt>
                    <dd className="min-w-0 font-mono text-xs break-all">{show(v)}</dd>
                  </div>
                ))}
            </dl>
          ) : (
            <p className="text-sm text-muted-foreground">
              This one has ended; MediaMTX no longer lists it.
            </p>
          )}
        </div>
      </SheetContent>
    </Sheet>
  )
}

function show(v: unknown): string {
  if (v === null || v === undefined) return '–'
  if (typeof v === 'string') return v || '–'
  if (typeof v === 'number') return Number.isInteger(v) ? String(v) : String(Number(v.toFixed(3)))
  if (typeof v === 'boolean') return String(v)
  return JSON.stringify(v)
}
