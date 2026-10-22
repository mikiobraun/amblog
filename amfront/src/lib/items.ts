// Entries prepared for display: verification status, own-post number,
// thread link. Shared by the timeline and thread views.

import type { Ref } from 'vue'
import { linkHref, type Entry } from './atom'
import { threadURL } from './agg'
import { ownPostNumber } from './api'
import { KeyCache, verifyStatus, type VerifyStatus } from './sign'

export interface Item {
  entry: Entry
  verified: VerifyStatus
  /** Post number if the entry is ours (directly, or re-served with via pointing to us). */
  ownSeq: number | null
  threadUrl: string
}

export function toItem(entry: Entry): Item {
  const alternate = linkHref(entry.links, 'alternate')
  return {
    entry,
    verified: { state: 'pending' },
    ownSeq: ownPostNumber(alternate) ?? ownPostNumber(linkHref(entry.links, 'via')),
    threadUrl: threadURL(alternate),
  }
}

// One cache for the whole app, so key documents are fetched once per author.
const keys = new KeyCache()

/**
 * Verifies items[start:] in the background; each badge updates when done.
 * Writes through the reactive array so Vue sees the change.
 */
export function verifyItems(items: Ref<Item[]>, start = 0): void {
  items.value.slice(start).forEach((item, i) => {
    verifyStatus(keys, item.entry).then((status) => {
      const target = items.value[start + i]
      if (target?.entry === item.entry) target.verified = status
    })
  })
}
