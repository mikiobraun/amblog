// JSON/text requests with bearer tokens, shared by the amfeed and amagg clients.

export interface RequestOptions {
  token?: string
  /** JSON request body. */
  json?: unknown
  /** Plain text request body. */
  text?: string
}

/**
 * Sends a request and returns the parsed JSON response (undefined for 204).
 * Non-2xx responses throw, with the server's error message if it sent one.
 */
export async function request<T>(method: string, url: string, opts: RequestOptions = {}): Promise<T | undefined> {
  const headers: Record<string, string> = {}
  if (opts.token) headers.Authorization = `Bearer ${opts.token}`
  let body: string | undefined
  if (opts.json !== undefined) {
    headers['Content-Type'] = 'application/json'
    body = JSON.stringify(opts.json)
  } else if (opts.text !== undefined) {
    headers['Content-Type'] = 'text/plain'
    body = opts.text
  }
  const resp = await fetch(url, { method, headers, body })
  if (!resp.ok) {
    let msg = `${resp.status} ${resp.statusText}`
    try {
      const err = await resp.json()
      if (err.error) msg += `: ${err.error}`
    } catch {
      // not JSON; keep the status line
    }
    throw new Error(`${method} ${url}: ${msg}`)
  }
  if (resp.status === 204) return undefined
  return (await resp.json()) as T
}
