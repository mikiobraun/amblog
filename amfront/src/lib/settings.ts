// Connection settings, kept in the browser. This is the only state amfront holds.

import { reactive, watch } from 'vue'

export interface Settings {
  /** Feed server base URL, e.g. https://miki.one */
  feedServer: string
  /** Bearer token for the feed server API. */
  token: string
  /** Atom feed to show as the timeline. Empty means the feed server's own feed. */
  timeline: string
}

const STORAGE_KEY = 'amfront.settings'

function load(): Settings {
  const defaults: Settings = { feedServer: '', token: '', timeline: '' }
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    return raw ? { ...defaults, ...JSON.parse(raw) } : defaults
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
  if (settings.timeline) return settings.timeline
  if (settings.feedServer) return settings.feedServer + '/feed.atom'
  return ''
}

export function isConfigured(): boolean {
  return settings.feedServer !== '' && settings.token !== ''
}

/** Strips a trailing slash, so URLs can be joined with "/path". */
export function normalizeBaseURL(url: string): string {
  return url.trim().replace(/\/+$/, '')
}
