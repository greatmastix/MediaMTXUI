import { useSyncExternalStore } from 'react'

import type { Role } from '@/lib/roles'

// Simple and expert modes. "streaming" is the simple shell: Streams and Watch, starting on Streams.
// "server" is everything, starting on the dashboard. Streamers only ever have the simple one. The choice is a
// per-browser convenience, kept in localStorage.

export type Mode = 'streaming' | 'server'

const key = 'mtxui-mode'
const listeners = new Set<() => void>()

function stored(): Mode {
  try {
    if (localStorage.getItem(key) === 'streaming') return 'streaming'
  } catch {
    // storage blocked: the default
  }
  return 'server'
}

/** The mode for a role: streamers are always in the simple one. */
export function modeFor(role: Role): Mode {
  return role === 'streamer' ? 'streaming' : stored()
}

export function setMode(mode: Mode) {
  try {
    localStorage.setItem(key, mode)
  } catch {
    // storage blocked: the choice lasts until reload
  }
  current = mode
  for (const l of listeners) l()
}

let current: Mode | undefined

function subscribe(l: () => void) {
  listeners.add(l)
  return () => listeners.delete(l)
}

/** The current mode for a role, re-rendering when it changes. */
export function useMode(role: Role): Mode {
  const mode = useSyncExternalStore(subscribe, () => (current ??= stored()))
  return role === 'streamer' ? 'streaming' : mode
}
