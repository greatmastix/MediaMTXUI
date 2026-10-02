export type Role = 'streamer' | 'viewer' | 'operator' | 'admin'

// A streamer sees only its own streams, so it sits below viewer.
const rank: Record<Role, number> = { streamer: 1, viewer: 2, operator: 3, admin: 4 }

/** Whether role includes floor; each role may do everything the roles below it may (as in the sidecar). */
export function atLeast(role: Role, floor: Role): boolean {
  return rank[role] >= rank[floor]
}
