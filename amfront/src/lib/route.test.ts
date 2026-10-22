import { expect, it } from 'vitest'
import { formatHash, parseHash } from './route'

it('round-trips pages and thread URLs through the hash', () => {
  const thread = 'https://agg.example/posts/12/thread.atom?x=1&y=2'
  for (const r of [
    { view: 'following' as const, thread: '' },
    { view: 'settings' as const, thread: '' },
    { view: 'timeline' as const, thread },
  ]) {
    expect(parseHash(formatHash(r))).toEqual(r)
  }
})

it('falls back to the start page for unknown hashes', () => {
  expect(parseHash('#/nope').thread).toBe('')
  expect(parseHash('#/thread').thread).toBe('')
  expect(['timeline', 'settings']).toContain(parseHash('').view)
})
