// Client for an aggregator (amagg): login, follows, feed reader passwords,
// and how to fetch feeds (with the session token, or through the proxy).

import { fetchAtom } from './atom'
import { publishChallenge } from './api'
import { request } from './http'
import { isLoggedIn, normalizeBaseURL, settings } from './settings'

interface LoginResponse {
  token: string
  name: string
  author_uri: string
  feed_url: string
  timeline_url: string
}

/**
 * Logs in to an aggregator, registering on first login: the aggregator issues
 * a challenge token, we publish "<token>.<aggregator origin>" on our feed
 * server, and the aggregator checks it under our identity URL. The posting
 * token never leaves this client.
 */
export async function login(aggregatorURL: string, invite: string): Promise<void> {
  const agg = normalizeBaseURL(aggregatorURL)
  const own = await fetchAtom(settings.feedServer + '/feed.atom')
  const authorURI = own.author?.uri
  if (!authorURI) throw new Error(`${settings.feedServer}/feed.atom has no author uri`)

  const challenge = (await request<{ token: string }>('POST', agg + '/api/login', {
    json: { author_uri: authorURI, invite },
  }))!
  // The origin binds the challenge to the aggregator we are talking to.
  await publishChallenge(challenge.token, `${challenge.token}.${new URL(agg).origin}`)
  const session = (await request<LoginResponse>('POST', agg + '/api/login/verify', {
    json: { token: challenge.token },
  }))!

  settings.aggregator = agg
  settings.aggToken = session.token
  settings.aggName = session.name
  settings.aggTimeline = session.timeline_url
  settings.source = 'home'
}

export function logout(): void {
  settings.aggregator = ''
  settings.aggToken = ''
  settings.aggName = ''
  settings.aggTimeline = ''
  if (settings.source === 'home' || settings.source === 'island') settings.source = 'feed'
}

function isAggregatorURL(url: string): boolean {
  return isLoggedIn() && url.startsWith(settings.aggregator + '/')
}

/**
 * Fetches an Atom document for display: aggregator feeds with the session
 * token; other feeds directly, or through the aggregator's proxy when logged
 * in (most feed servers don't send CORS headers).
 */
export function fetchFeed(url: string) {
  if (isAggregatorURL(url)) return fetchAtom(url, settings.aggToken)
  if (isLoggedIn() && !url.startsWith(settings.feedServer + '/')) {
    return fetchAtom(`${settings.aggregator}/api/feed?url=${encodeURIComponent(url)}`, settings.aggToken)
  }
  return fetchAtom(url)
}

/** The thread feed for an entry the aggregator serves, or "" for other entries. */
export function threadURL(alternate: string): string {
  return isAggregatorURL(alternate) && /\/posts\/\d+$/.test(alternate) ? alternate + '/thread.atom' : ''
}

function api<T>(method: string, path: string, json?: unknown) {
  return request<T>(method, settings.aggregator + path, { token: settings.aggToken, json })
}

export interface FollowedFeed {
  feed_url: string
  title: string
  html_url?: string
}

export async function follow(url: string): Promise<FollowedFeed> {
  return (await api<FollowedFeed>('POST', '/api/follow', { feed_url: url }))!
}

export async function unfollow(feedURL: string): Promise<void> {
  await api('DELETE', '/api/follow?feed_url=' + encodeURIComponent(feedURL))
}

export async function following(): Promise<FollowedFeed[]> {
  return (await api<{ feeds: FollowedFeed[] }>('GET', '/api/following'))!.feeds
}

export interface Reader {
  id: number
  name: string
  created: string
}

export interface ReaderCredential extends Reader {
  username: string
  password: string
  timeline_url: string
}

export async function createReader(name: string): Promise<ReaderCredential> {
  return (await api<ReaderCredential>('POST', '/api/readers', { name }))!
}

export async function readers(): Promise<Reader[]> {
  return (await api<{ readers: Reader[] }>('GET', '/api/readers'))!.readers
}

export async function deleteReader(id: number): Promise<void> {
  await api('DELETE', `/api/readers/${id}`)
}
