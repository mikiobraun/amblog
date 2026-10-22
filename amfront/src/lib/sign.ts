// Entry signature verification, mirroring internal/sign in the Go code.
// See the spec, "Entry Signing".

import type { Entry } from './atom'

export const ALGORITHM = 'ed25519'
/** The key document's path relative to the author URI. */
export const KEYS_PATH = 'atomic-keys'

export interface Key {
  id: string
  algorithm: string
  public_key: string
  created: string
  expires: string | null
}

export interface KeyDoc {
  keys: Key[]
}

const escapes: Record<string, string> = { '\\': '\\\\', '\n': '\\n', '\r': '\\r' }

function escape(s: string): string {
  return s.replace(/[\\\n\r]/g, (c) => escapes[c])
}

/** Builds the signing input for an entry. Throws if the entry cannot be signed. */
export function signingInput(e: Entry): string {
  if (!e.author?.uri) {
    throw new Error('entry has no author uri')
  }
  let contentType = ''
  let content = ''
  if (e.content) {
    contentType = e.content.type || 'text' // RFC 4287 default
    if (contentType !== 'text' && contentType !== 'html') {
      throw new Error(`content type "${contentType}" cannot be signed, only text or html`)
    }
    content = e.content.body
  }
  const fields: [string, string][] = [
    ['id', e.id],
    ['author', e.author.uri],
    ['published', e.published],
    ['updated', e.updated],
    ['in-reply-to', e.inReplyTo?.ref ?? ''],
    ['verb', e.verb],
    ['title', e.title],
    ['summary', e.summary],
    ['content-type', contentType],
    ['content', content],
  ]
  for (const l of e.links) {
    if (l.rel === 'enclosure') {
      fields.push(['enclosure', l.href])
    }
  }
  return fields.map(([name, value]) => `${name}: ${escape(value)}`).join('\n')
}

/**
 * Returns the key document URL for an author URI: KEYS_PATH appended to the
 * URI's path, so identities with a path (https://host.example/alice) get their
 * own key document.
 */
export function keysURL(authorURI: string): string {
  const u = new URL(authorURI)
  if (u.search !== '' || u.hash !== '') {
    throw new Error(`author uri "${authorURI}" has a query or fragment`)
  }
  u.pathname = u.pathname.replace(/\/$/, '') + '/' + KEYS_PATH
  return u.toString()
}

function base64Bytes(s: string): Uint8Array<ArrayBuffer> {
  const bin = atob(s)
  const bytes = new Uint8Array(bin.length)
  for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i)
  return bytes
}

/** Thrown when the browser can't verify at all (no Web Crypto outside secure contexts). */
export class UnverifiableError extends Error {}

/** Verifies the entry's signature against a key document. Throws with the reason on failure. */
export async function verify(e: Entry, keys: KeyDoc): Promise<void> {
  if (!globalThis.crypto?.subtle) {
    throw new UnverifiableError('Web Crypto unavailable (needs https or localhost)')
  }
  if (!e.signature) {
    throw new Error('not signed')
  }
  if (e.signature.algorithm !== ALGORITHM) {
    throw new Error(`unsupported algorithm "${e.signature.algorithm}"`)
  }
  const key = keys.keys.find((k) => k.id === e.signature!.keyId)
  if (!key) {
    throw new Error(`unknown key id "${e.signature.keyId}"`)
  }
  if (key.algorithm !== ALGORITHM) {
    throw new Error(`key "${key.id}" has unsupported algorithm "${key.algorithm}"`)
  }
  const pub = await crypto.subtle.importKey('raw', base64Bytes(key.public_key), { name: 'Ed25519' }, false, [
    'verify',
  ])
  const data = new TextEncoder().encode(signingInput(e))
  const ok = await crypto.subtle.verify({ name: 'Ed25519' }, pub, base64Bytes(e.signature.value), data)
  if (!ok) {
    throw new Error('signature does not match')
  }
}

/** Fetches key documents, once per author URI. */
export class KeyCache {
  private docs = new Map<string, Promise<KeyDoc>>()

  get(authorURI: string): Promise<KeyDoc> {
    let doc = this.docs.get(authorURI)
    if (!doc) {
      doc = fetchKeys(authorURI)
      // Forget failures so a later attempt can retry.
      doc.catch(() => this.docs.delete(authorURI))
      this.docs.set(authorURI, doc)
    }
    return doc
  }

  async verify(e: Entry): Promise<void> {
    if (!e.author?.uri) {
      throw new Error('entry has no author uri')
    }
    await verify(e, await this.get(e.author.uri))
  }
}

async function fetchKeys(authorURI: string): Promise<KeyDoc> {
  const url = keysURL(authorURI)
  const resp = await fetch(url)
  if (!resp.ok) {
    throw new Error(`GET ${url}: ${resp.status} ${resp.statusText}`)
  }
  return (await resp.json()) as KeyDoc
}
