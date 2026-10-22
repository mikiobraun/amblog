import { reactive } from 'vue'
import { isConfigured } from './settings'

// The current page lives in the URL hash, so the browser's back and forward
// buttons work and pages can be reloaded or bookmarked:
//
//   #/timeline   #/following   #/settings   #/thread?url=<thread feed URL>
//
// Navigating sets location.hash (a new history entry); the hashchange event
// updates `route`. No hash means the start page.

export type View = 'timeline' | 'following' | 'settings'

export interface Route {
  view: View
  /** Thread feed shown on the timeline page, or "". */
  thread: string
}

const views: View[] = ['timeline', 'following', 'settings']

export function parseHash(hash: string): Route {
  const [path, query = ''] = hash.replace(/^#\/?/, '').split('?', 2)
  if (path === 'thread') {
    const url = new URLSearchParams(query).get('url')
    if (url) return { view: 'timeline', thread: url }
  }
  if ((views as string[]).includes(path)) return { view: path as View, thread: '' }
  return { view: isConfigured() ? 'timeline' : 'settings', thread: '' }
}

export function formatHash(r: Route): string {
  if (r.thread) return '#/thread?' + new URLSearchParams({ url: r.thread }).toString()
  return '#/' + r.view
}

export const route = reactive<Route>(parseHash(location.hash))

window.addEventListener('hashchange', () => Object.assign(route, parseHash(location.hash)))

export function go(view: View, thread = '') {
  location.hash = formatHash({ view, thread })
}
