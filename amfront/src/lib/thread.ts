import type { Entry } from './atom'

/**
 * Reply depth of each entry in a thread feed, which lists entries in
 * conversation order (parents before replies). Entries whose parent isn't in
 * the feed are at depth 0. Threads are matched by in-reply-to ref.
 */
export function threadDepths(entries: Entry[]): number[] {
  const depth = new Map<string, number>()
  return entries.map((e) => {
    const parent = e.inReplyTo ? depth.get(e.inReplyTo.ref) : undefined
    const d = parent === undefined ? 0 : parent + 1
    depth.set(e.id, d)
    return d
  })
}
