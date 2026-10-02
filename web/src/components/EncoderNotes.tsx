import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useEffect } from 'react'

import { streamNotesQuery, streamQuery, type Stream } from '@/api/streams'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { useNow } from '@/hooks/useNow'
import { formatSince } from '@/lib/format'

// What MediaMTX's log said about this stream's encoder in the last minutes, which nothing else shows: MediaMTX refused
// it (its tracks do not match the holding clip), the holding screen switched to the version with its audio, or
// browsers cannot play its B-frames over WebRTC.

export function EncoderNotes({ stream: s }: { stream: Stream }) {
  const queryClient = useQueryClient()
  const notes = useQuery(streamNotesQuery(s.id))
  const now = useNow(10_000)
  // The server switched the holding version: the stream's settings on this page are out of date.
  const switched = notes.data?.find((n) => n.kind === 'switched')?.at
  useEffect(() => {
    if (switched)
      void queryClient.invalidateQueries({ queryKey: streamQuery(s.id).queryKey, exact: true })
  }, [switched, queryClient, s.id])
  if (!notes.data?.length) return null
  return (
    <div className="space-y-2" data-testid="encoder-notes">
      {notes.data.map((n) => (
        <Alert key={n.kind + n.message} role="status">
          <AlertTitle>
            {{ tracks: 'Your encoder was refused', switched: 'Holding screen switched' }[n.kind] ??
              'B-frames from your encoder'}{' '}
            <span className="font-normal text-muted-foreground">({formatSince(n.at, now)})</span>
          </AlertTitle>
          <AlertDescription>{n.message}</AlertDescription>
        </Alert>
      ))}
    </div>
  )
}
