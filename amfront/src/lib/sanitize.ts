import DOMPurify from 'dompurify'

// HTML content comes from arbitrary feeds and is rendered in our origin, where
// the token lives, so it must be sanitized. Links open in a new tab and carry
// no ranking (rel="nofollow ugc", see the spec, "Spam Control").
DOMPurify.addHook('afterSanitizeAttributes', (node) => {
  if (node.tagName === 'A') {
    node.setAttribute('target', '_blank')
    node.setAttribute('rel', 'nofollow ugc noopener')
  }
})

export function sanitize(html: string): string {
  return DOMPurify.sanitize(html)
}
