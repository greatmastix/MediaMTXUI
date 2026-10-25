// Scratch files in the browser's own storage (the origin private file system), for media too large to hold in
// memory: a recording stretch fetched to become a holding clip, and the clips made from it, up to gigabytes each.
// Where the browser has none, they stay in memory, as small clips always did.

const prefix = 'mtxui-scratch-'

/** Whether the browser keeps files of its own (Chrome, Edge, Firefox, Safari). */
export function hasScratch(): boolean {
  return (
    typeof navigator !== 'undefined' &&
    'storage' in navigator &&
    'getDirectory' in navigator.storage
  )
}

/** A new, empty scratch file. */
export async function newScratch(): Promise<FileSystemFileHandle> {
  const dir = await navigator.storage.getDirectory()
  return dir.getFileHandle(`${prefix}${crypto.randomUUID()}.mp4`, { create: true })
}

/** Removes every scratch file, also those an attempt left behind that did not get to remove its own. */
export async function clearScratch(): Promise<void> {
  if (!hasScratch()) return
  const dir = await navigator.storage.getDirectory()
  const names: string[] = []
  for await (const name of dir.keys()) if (name.startsWith(prefix)) names.push(name)
  await Promise.all(names.map((n) => dir.removeEntry(n).catch(() => undefined)))
}
