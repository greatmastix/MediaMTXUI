import { useEffect, useState, type ReactNode } from 'react'

import { LiveClient, type LiveHandlers } from './client'
import { LiveContext } from './useLive'

/** Runs the live event stream while mounted: inside the signed-in part of the app only. */
export function LiveProvider({
  children,
  onSessionEnded,
  onRefused,
  client: given,
}: { children: ReactNode; client?: LiveClient } & LiveHandlers) {
  const [client] = useState(() => given ?? new LiveClient())
  useEffect(() => {
    client.setHandlers({ onSessionEnded, onRefused })
  }, [client, onSessionEnded, onRefused])
  useEffect(() => {
    client.start()
    return () => {
      client.stop()
    }
  }, [client])
  return <LiveContext value={client}>{children}</LiveContext>
}
