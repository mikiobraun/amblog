// Client for the user's own feed server API (amfeed).

import { request } from './http'
import { settings } from './settings'

export interface InReplyTo {
  ref: string
  href: string
}

export interface CreateEntry {
  content: string
  content_type: 'text' | 'html'
  title?: string
  in_reply_to?: InReplyTo
}

export interface CreatedEntry {
  seq: number
  id: string
  url: string
}

export async function createEntry(req: CreateEntry): Promise<CreatedEntry> {
  return (await request<CreatedEntry>('POST', settings.feedServer + '/api/entries', {
    token: settings.token,
    json: req,
  }))!
}

export async function deleteEntry(seq: number): Promise<void> {
  await request('DELETE', `${settings.feedServer}/api/entries/${seq}`, { token: settings.token })
}

/** Publishes a login challenge for an aggregator under our identity URL. */
export async function publishChallenge(token: string, value: string): Promise<void> {
  await request('PUT', `${settings.feedServer}/api/challenges/${token}`, { token: settings.token, text: value })
}

/**
 * Returns the post number if the URL is a post on our own feed server
 * (`<feedServer>/posts/<n>`), otherwise null.
 */
export function ownPostNumber(url: string): number | null {
  const prefix = settings.feedServer + '/posts/'
  if (!settings.feedServer || !url.startsWith(prefix)) return null
  const rest = url.slice(prefix.length)
  return /^\d+$/.test(rest) ? Number(rest) : null
}
