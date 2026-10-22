// Atom (RFC 4287) parsing for the subset Atomic Micro Blogging uses,
// mirroring internal/atom in the Go code. Timestamps stay literal strings,
// since they are part of the signing input.

export const ATOM_NS = 'http://www.w3.org/2005/Atom'
export const THREAD_NS = 'http://purl.org/syndication/thread/1.0'
export const ATOMIC_NS = 'https://atomicmicro.blog/ns/1.0'
export const ATOM_MEDIA_TYPE = 'application/atom+xml'

export interface Person {
  name: string
  uri: string
}

export interface Link {
  rel: string
  href: string
  type: string
}

export interface Content {
  type: string
  body: string
}

export interface InReplyTo {
  ref: string
  href: string
}

export interface Signature {
  keyId: string
  algorithm: string
  value: string
}

export interface Entry {
  id: string
  title: string
  summary: string
  published: string
  updated: string
  author: Person | null
  content: Content | null
  links: Link[]
  inReplyTo: InReplyTo | null
  /** RFC 4685 thr:total: replies the publisher knows, null if not given. */
  total: number | null
  verb: string
  signature: Signature | null
}

export interface Feed {
  id: string
  title: string
  author: Person | null
  links: Link[]
  entries: Entry[]
}

function children(el: Element, ns: string, name: string): Element[] {
  return Array.from(el.children).filter((c) => c.namespaceURI === ns && c.localName === name)
}

function child(el: Element, ns: string, name: string): Element | null {
  return children(el, ns, name)[0] ?? null
}

function text(el: Element, name: string): string {
  return child(el, ATOM_NS, name)?.textContent ?? ''
}

function parseLinks(el: Element): Link[] {
  return children(el, ATOM_NS, 'link').map((l) => ({
    rel: l.getAttribute('rel') ?? '',
    href: l.getAttribute('href') ?? '',
    type: l.getAttribute('type') ?? '',
  }))
}

function parsePerson(el: Element | null): Person | null {
  return el && { name: text(el, 'name'), uri: text(el, 'uri') }
}

function parseEntry(el: Element): Entry {
  const content = child(el, ATOM_NS, 'content')
  const inReplyTo = child(el, THREAD_NS, 'in-reply-to')
  const signature = child(el, ATOMIC_NS, 'signature')
  const total = child(el, THREAD_NS, 'total')?.textContent?.trim() ?? ''
  return {
    id: text(el, 'id'),
    title: text(el, 'title'),
    summary: text(el, 'summary'),
    published: text(el, 'published'),
    updated: text(el, 'updated'),
    author: parsePerson(child(el, ATOM_NS, 'author')),
    content: content && { type: content.getAttribute('type') ?? '', body: content.textContent ?? '' },
    links: parseLinks(el),
    inReplyTo: inReplyTo && {
      ref: inReplyTo.getAttribute('ref') ?? '',
      href: inReplyTo.getAttribute('href') ?? '',
    },
    total: /^\d+$/.test(total) ? Number(total) : null,
    verb: child(el, ATOMIC_NS, 'verb')?.textContent ?? '',
    signature: signature && {
      keyId: signature.getAttribute('key-id') ?? '',
      algorithm: signature.getAttribute('algorithm') ?? '',
      value: signature.getAttribute('value') ?? '',
    },
  }
}

/** Parses an Atom feed or entry document. An entry document becomes a feed with just that entry. */
export function parseAtom(xml: string): Feed {
  const doc = new DOMParser().parseFromString(xml, 'application/xml')
  const root = doc.documentElement
  if (root.getElementsByTagName('parsererror').length > 0 || root.localName === 'parsererror') {
    throw new Error('not well-formed XML')
  }
  if (root.namespaceURI !== ATOM_NS) {
    throw new Error(`not an Atom document (root <${root.localName}> in namespace ${root.namespaceURI})`)
  }
  if (root.localName === 'entry') {
    return { id: '', title: '', author: null, links: [], entries: [parseEntry(root)] }
  }
  if (root.localName !== 'feed') {
    throw new Error(`unexpected root element <${root.localName}>`)
  }
  return {
    id: text(root, 'id'),
    title: text(root, 'title'),
    author: parsePerson(child(root, ATOM_NS, 'author')),
    links: parseLinks(root),
    entries: children(root, ATOM_NS, 'entry').map(parseEntry),
  }
}

/** Returns the href of the first link with the given rel. A link without rel counts as "alternate". */
export function linkHref(links: Link[], rel: string): string {
  const l = links.find((l) => l.rel === rel || (l.rel === '' && rel === 'alternate'))
  return l?.href ?? ''
}

/** Fetches and parses an Atom document, with a bearer token if given. */
export async function fetchAtom(url: string, token = ''): Promise<Feed> {
  const headers: Record<string, string> = { Accept: ATOM_MEDIA_TYPE }
  if (token) headers.Authorization = `Bearer ${token}`
  const resp = await fetch(url, { headers })
  if (!resp.ok) {
    throw new Error(`GET ${url}: ${resp.status} ${resp.statusText}`)
  }
  return parseAtom(await resp.text())
}
