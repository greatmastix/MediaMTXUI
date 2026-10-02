import { useLive } from '@/live/useLive'

/** A stream's live state from the event stream: online, available (holding screen), viewers, bitrate in. */
export function useStreamLive(name: string) {
  const live = useLive()
  const path = live.lists.paths?.items.get(name)
  return {
    known: live.lists.paths !== undefined,
    online: path?.online ?? false,
    /** Something plays: the stream, or its holding screen while nobody streams. */
    available: path?.available ?? false,
    viewers: path?.readers?.length ?? 0,
    inBps: live.rates.get(name)?.inBps,
    ratesAt: live.ratesAt,
    path,
  }
}
