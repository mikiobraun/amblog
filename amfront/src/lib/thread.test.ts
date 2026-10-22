import { expect, it } from 'vitest'
import type { Entry } from './atom'
import { threadDepths } from './thread'

function entry(id: string, parent?: string): Entry {
  return {
    id,
    title: '',
    summary: '',
    published: '',
    updated: '',
    author: null,
    content: null,
    links: [],
    inReplyTo: parent ? { ref: parent, href: '' } : null,
    total: null,
    verb: '',
    signature: null,
  }
}

it('computes reply depths in conversation order', () => {
  const entries = [entry('a', 'unknown'), entry('b', 'a'), entry('c', 'b'), entry('d', 'a')]
  expect(threadDepths(entries)).toEqual([0, 1, 2, 1])
})
