// Client for the user's own feed server API (amfeed).

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

async function request<T>(method: string, path: string, body?: unknown): Promise<T | undefined> {
  const headers: Record<string, string> = { Authorization: `Bearer ${settings.token}` }
  if (body !== undefined) headers['Content-Type'] = 'application/json'
  const resp = await fetch(settings.feedServer + path, {
    method,
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  if (!resp.ok) {
    let msg = `${resp.status} ${resp.statusText}`
    try {
      const err = await resp.json()
      if (err.error) msg += `: ${err.error}`
    } catch {
      // not JSON; keep the status line
    }
    throw new Error(`${method} ${path}: ${msg}`)
  }
  if (resp.status === 204) return undefined
  return (await resp.json()) as T
}

export async function createEntry(req: CreateEntry): Promise<CreatedEntry> {
  return (await request<CreatedEntry>('POST', '/api/entries', req))!
}

export async function deleteEntry(seq: number): Promise<void> {
  await request('DELETE', `/api/entries/${seq}`)
}

/**
 * Returns the post number if the entry's alternate link is a post on our own
 * feed server (`<feedServer>/posts/<n>`), otherwise null.
 */
export function ownPostNumber(alternate: string): number | null {
  const prefix = settings.feedServer + '/posts/'
  if (!settings.feedServer || !alternate.startsWith(prefix)) return null
  const rest = alternate.slice(prefix.length)
  return /^\d+$/.test(rest) ? Number(rest) : null
}
