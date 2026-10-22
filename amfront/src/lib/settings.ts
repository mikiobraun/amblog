// Connection settings, kept in the browser. This is the only state amfront holds.

import { reactive, watch } from 'vue'

/** Which feed the timeline shows. */
export type TimelineSource = 'feed' | 'home' | 'island' | 'custom'

export interface Settings {
  /** Feed server base URL, e.g. https://miki.one */
  feedServer: string
  /** Bearer token for the feed server API. */
  token: string
  /** A custom Atom feed URL, shown when source is "custom". */
  timeline: string
  source: TimelineSource
  /** Aggregator base URL, e.g. https://capyclub.example; empty if not logged in. */
  aggregator: string
  /** Aggregator session token, from the login flow. */
  aggToken: string
  /** Member name on the aggregator. */
  aggName: string
  /** The member's timeline feed on the aggregator. */
  aggTimeline: string
}

const STORAGE_KEY = 'amfront.settings'

function load(): Settings {
  const defaults: Settings = {
    feedServer: '',
    token: '',
    timeline: '',
    source: 'feed',
    aggregator: '',
    aggToken: '',
    aggName: '',
    aggTimeline: '',
  }
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    if (!raw) return defaults
    const stored = JSON.parse(raw)
    // Settings from before timeline sources: a timeline URL meant "custom".
    if (stored.source === undefined && stored.timeline) stored.source = 'custom'
    return { ...defaults, ...stored }
  } catch {
    return defaults
  }
}

export const settings = reactive<Settings>(load())

watch(settings, (s) => {
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(s))
  } catch {
    // Storage unavailable (private mode etc.): settings last for this page load only.
  }
})

export function timelineURL(): string {
  switch (settings.source) {
    case 'home':
      return settings.aggTimeline
    case 'island':
      return settings.aggregator ? settings.aggregator + '/timeline.atom' : ''
    case 'custom':
      return settings.timeline
    case 'feed':
      return settings.feedServer ? settings.feedServer + '/feed.atom' : ''
  }
}

export function isConfigured(): boolean {
  return settings.feedServer !== '' && settings.token !== ''
}

export function isLoggedIn(): boolean {
  return settings.aggregator !== '' && settings.aggToken !== ''
}

/** Strips a trailing slash, so URLs can be joined with "/path". */
export function normalizeBaseURL(url: string): string {
  return url.trim().replace(/\/+$/, '')
}
