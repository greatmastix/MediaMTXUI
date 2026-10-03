import { useQuery } from '@tanstack/react-query'
import { Radio } from 'lucide-react'

import { publicStreamQuery } from '@/api/streams'
import { Player } from '@/components/Player'
import { StatusPill } from '@/components/StatusPill'

// A public stream's watch link: anyone can open it, no account. It plays over WebRTC (falling back to HLS) through
// the public endpoints, which serve public streams only; a private or unknown stream looks the same as none. The
// heading is the stream's title from the server, never the address: anyone can make a link with any text in it.

export function PublicWatchPage({ name }: { name: string }) {
  const q = useQuery(publicStreamQuery(name))
  const s = q.data
  return (
    <div className="mx-auto max-w-5xl space-y-4 p-4 md:p-8">
      <header className="flex flex-wrap items-center gap-3">
        <span className="grid size-8 shrink-0 place-items-center rounded-[9px] bg-signal/15">
          <Radio className="size-[18px] text-signal" aria-hidden />
        </span>
        <h1 className="font-heading text-2xl font-semibold">{s?.title ?? 'Watch'}</h1>
        {s &&
          (s.live ? (
            <StatusPill tone="good" data-testid="public-state" data-state="live">
              Live
            </StatusPill>
          ) : s.available ? (
            <StatusPill tone="neutral" data-testid="public-state" data-state="holding">
              Back soon
            </StatusPill>
          ) : (
            <StatusPill tone="neutral" data-testid="public-state" data-state="offline">
              Offline
            </StatusPill>
          ))}
      </header>
      {q.isError ? (
        <p className="rounded-lg border border-dashed p-8 text-center text-sm text-muted-foreground">
          There is no public stream at this address.
        </p>
      ) : s?.live || s?.available ? (
        <Player path={s.name} public className="w-full" />
      ) : (
        <p className="grid aspect-video place-items-center rounded-lg border border-dashed p-8 text-center text-sm text-muted-foreground">
          {s
            ? 'Not live right now. The video starts here by itself when it goes live.'
            : 'Loading…'}
        </p>
      )}
    </div>
  )
}
