import { createContext, use, useSyncExternalStore } from 'react'

import type { LiveClient } from './client'
import { initialState, type LiveState } from './store'

export const LiveContext = createContext<LiveClient | null>(null)

const noClient = {
  subscribe: () => () => undefined,
  getState: () => initialState,
}

/** The live state; re-renders on every change. Outside a LiveProvider it stays at the initial state. */
export function useLive(): LiveState {
  const client = use(LiveContext) ?? noClient
  return useSyncExternalStore(client.subscribe, client.getState)
}
