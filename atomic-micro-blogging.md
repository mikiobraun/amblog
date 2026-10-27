# Atomic Micro Blogging

**Domain:** atomicmicro.blog  
**Language:** Go  
**Status:** Draft Specification

---

## 1. Overview

Atomic Micro Blogging is a decentralized social network built on existing web standards. The core insight is that social networking decomposes into three independent concerns — **publishing**, **aggregation/curation**, and **presentation** — and that coupling them into a single server (as Mastodon, Bluesky, and traditional platforms do) is an architectural mistake.

Each layer is independently swappable:

```
┌─────────────┐     ┌─────────────┐     ┌─────────────┐
│   amfront    │────▶│    amagg    │────▶│   amfeed    │
│  (frontend)  │     │ (aggregator) │     │(feed server) │
└─────────────┘     └─────────────┘     └─────────────┘
   Pure client        Social graph        Static Atom
   Any UI/UX          Timeline algo       Your content
   Swappable          Competing           Your identity
```

- **amfeed** — You own your content. A feed server that publishes Atom feeds and accepts new posts via token-authenticated API. Your identity URL.
- **amagg** — Someone runs the room. An aggregator that holds follow graphs, merges subscribed feeds into timelines, handles interactions via WebMention, and serves the merged result as Atom.
- **amfront** — Anyone can build a face. A pure client that reads from aggregators and posts to feed servers. Zero server state.

### Design Principles

The rest of this document follows from these.

1. **Separate the jobs.** Publishing, aggregation and presentation are independent services that only talk through open formats. Each can be replaced without the others noticing.
2. **Reuse existing standards.** Atom, Atom threading, paged feeds, WebSub, WebMention, OPML. Our own extensions are few and live in the `atomic:` namespace.
3. **Identity is a URL, and whoever controls it owns the identity.** Everything that speaks for you (keys, login proofs, following lists) lives under your identity URL, derived from it by fixed paths, not discovered through links someone else could serve. An identity URL is used in full; it is never reduced to its host.
4. **Your data lives with you, not with the aggregator.** Posts and shared follow lists sit under your identity, so leaving an aggregator loses nothing. Aggregators never hold your posting token and never write to your identity.
5. **Sign the content, not the transport.** Signatures cover what the author said. Links and provenance are unsigned, so aggregators can re-serve entries under their own URLs without breaking them.
6. **Verify what you can, label what you can't.** Signed entries are checked; everything else is accepted and shown as unverified, with where it came from. One rule for every path instead of special cases.
7. **Islands by default.** An aggregator is its own space with its own rules. Content and conversations cross between islands only through explicit actions.
8. **Credentials are always explicit.** Bearer tokens, or HTTP Basic for feed readers, never cookies. That keeps `Access-Control-Allow-Origin: *` safe everywhere.
9. **Simple and explicit over clever.** Uniform rules, fixed formats, loud failures. Unknown config keys, malformed cursors and unsignable content are errors, not guesses.
10. **Take away the payoff instead of building gates.** Abuse is handled by making it worthless and easy to remove (no link ranking, reach limited to one thread, rate limits, hiding and blocking), not by hurdles that honest senders using plain standards can't pass.

### Prior Art

This architecture is spiritually closest to **OStatus** (Atom + ActivityStreams + PubSubHubbub + Salmon + WebFinger), which powered GNU Social and early Mastodon. The key difference: OStatus baked the social graph into each server instance. We separate it into the aggregator layer, enabling competing aggregators over the same content — analogous to how podcast apps compete over the same RSS feeds.

### Existence Proof

Podcasting proves that decentralized Atom/RSS-based content distribution works at internet scale, with competing clients, directories, and recommendation engines all operating over the same feed infrastructure without any central protocol authority.

---

## 2. Protocol Stack

The base is existing W3C and IETF standards. On top of that we add a few small extensions in the `atomic:` namespace (`https://atomicmicro.blog/ns/1.0`).

| Layer | Standard | Role |
|-------|----------|------|
| Content format | [Atom (RFC 4287)](https://tools.ietf.org/html/rfc4287) | Base format for all posts |
| Provenance | Atom `atom:source`, `link rel="via"` (RFC 4287) | Entries re-served by aggregators keep their origin |
| Threading | [Atom Threading Extensions (RFC 4685)](https://tools.ietf.org/html/rfc4685) | `thr:in-reply-to`, `thr:total`, reply feeds |
| Real-time push | [WebSub (W3C)](https://www.w3.org/TR/websub/) | Feed server → aggregator push notifications |
| Interactions | [WebMention (W3C)](https://www.w3.org/TR/webmention/) | Cross-aggregator replies, likes, reposts |
| Content types | Atom extensions | Micro-posts, replies, media, long-form (extensible) |

### What We Add

- **Entry signing** — Ed25519 signatures over a fixed set of entry fields, for tamper evidence
- **Token auth** — Simple bearer tokens for posting to your own feed (no OAuth)
- **Login challenge** — Proving ownership of an identity URL to an aggregator
- **Verbs** — `atomic:verb` for likes and reposts
- **Per-aggregator feeds** — Contextual identity across aggregators

### Interoperability Goals

Interop with other social protocols (ActivityPub, OStatus, AT Protocol) is a non-goal. Interop that does matter:

- **Feed readers** — every feed is a valid Atom feed, readable by any feed reader.
- **WebMention-enabled blogs** — replies to blog posts are delivered via standard WebMention, and blogs can send WebMentions to us (see [Spam Control](#spam-control)).

### CORS

Feed servers and aggregators SHOULD send `Access-Control-Allow-Origin: *` on feeds and APIs. This is safe because authentication is always explicit (bearer tokens, or HTTP Basic for feed readers), never cookies, and servers never send `Access-Control-Allow-Credentials`, so browsers never attach credentials to cross-origin requests.

---

## 3. Identity Model

**Identity = URL.** Your author URI, the URL of your profile page, *is* your identity. It's the `author` `uri` on every entry you sign; your feed is linked from it. No handles, no DID documents, no WebFinger indirection.

```
https://miki.one/                 ← identity URL: profile page, links your feed
https://miki.one/feed.atom        ← your Atom feed
https://miki.one/atomic-keys      ← signing public key(s)
https://miki.one/atomic-challenge/{token}  ← temporary proof of ownership for aggregator login
https://miki.one/following/{aggregator}.opml  ← shared following list per aggregator (opt-in)
```

An identity URL MAY have a path. On a shared host, `https://amhost.example/mikio/` is a full identity, and nothing in the protocol reduces it to its host: the key document, entry IDs and thread matching all work on the full URL. Identity URLs MUST NOT carry a query or fragment.

### Profile Page

The profile page at the identity URL is a simple HTML page that:
- Displays author info (name, bio, avatar)
- Links to the Atom feed (`<link rel="alternate" type="application/atom+xml">`)
- Optionally lists aggregators the user participates in
- Links to the signing key endpoint

### Signing Keys

Published at `<author URI>/atomic-keys` (the path `atomic-keys` appended to the author URI's path, ignoring a trailing slash) as a JSON document. So `https://miki.one/` → `https://miki.one/atomic-keys` and `https://amhost.example/mikio/` → `https://amhost.example/mikio/atomic-keys`.

The location derives from the signed `author` field alone, so only whoever controls the identity URL can publish keys for it, and identities sharing a host have separate key documents. On a shared host, the host operator controls the keys (signing is custodial there).

```json
{
  "keys": [
    {
      "id": "2024-01",
      "algorithm": "ed25519",
      "public_key": "base64-encoded-public-key",
      "created": "2024-01-15T00:00:00Z",
      "expires": null
    }
  ]
}
```

Key rotation: publish a new key, start signing with it, keep old keys listed for verification of historical entries. Aggregators cache keys and re-fetch on verification failure.

### Per-Aggregator Identity

A user MAY maintain different feeds for different aggregators — a professional feed and a casual feed, for instance. Each is a separate Atom feed at a separate path, each representing a facet of the same identity URL.

```
https://miki.one/feeds/tech.atom     ← subscribed on tech-aggregator.example
https://miki.one/feeds/shitpost.atom ← subscribed on funposting.example
```

Both resolve back to `https://miki.one/` as the author identity.

---

## 4. amfeed — Feed Server

The feed server is the simplest component. It stores your posts and serves them as Atom. Think of it as a blog engine with a POST endpoint.

### Responsibilities

1. Serve Atom feed(s) at stable URLs
2. Accept new entries via authenticated API
3. Serve individual entry permalinks
4. Publish signing keys
5. Receive WebMentions for its entries and serve them as reply feeds, moderated by the owner
6. Send WebMentions for its own replies
7. Act as the WebSub hub for its feed, pushing updates to subscribed aggregators
8. Publish login challenges, so the owner can prove ownership of the identity to aggregators
9. Optionally, publish Markdown files from a folder as posts (see [Content Folder](#content-folder))

### Data Model

An entry is an Atom entry (RFC 4287) with extensions:

```xml
<?xml version="1.0" encoding="utf-8"?>
<feed xmlns="http://www.w3.org/2005/Atom"
      xmlns:thr="http://purl.org/syndication/thread/1.0">
  <title>Mikio's Feed</title>
  <link href="https://miki.one/feed.atom" rel="self"/>
  <link href="https://miki.one/" rel="alternate"/>
  <link href="https://miki.one/websub" rel="hub"/>
  <id>https://miki.one/feed.atom</id>
  <updated>2024-12-01T12:00:00Z</updated>
  <author>
    <name>Mikio</name>
    <uri>https://miki.one/</uri>
  </author>

  <entry>
    <id>tag:miki.one,2024-12:post-42</id>
    <title type="text">Just discovered RFC 4685 has threading built in</title>
    <content type="html">&lt;p&gt;Just discovered RFC 4685 has threading
      built in. Why did we ever invent custom threading?&lt;/p&gt;</content>
    <link href="https://miki.one/posts/42" rel="alternate"/>
    <published>2024-12-01T12:00:00Z</published>
    <updated>2024-12-01T12:00:00Z</updated>

    <!-- Entry signature -->
    <atomic:signature xmlns:atomic="https://atomicmicro.blog/ns/1.0"
                      key-id="2024-01"
                      algorithm="ed25519"
                      value="base64-signature-of-canonical-entry"/>
  </entry>

  <!-- A reply to someone else's post -->
  <entry>
    <id>tag:miki.one,2024-12:post-43</id>
    <content type="html">&lt;p&gt;Totally agree, this is elegant.&lt;/p&gt;</content>
    <link href="https://miki.one/posts/43" rel="alternate"/>

    <!-- RFC 4685: threading -->
    <thr:in-reply-to
      ref="tag:other.example,2024-12:post-99"
      href="https://other.example/posts/99"
      type="text/html"/>

    <!-- RFC 4685: link to reply feed for this entry -->
    <link rel="replies"
          href="https://miki.one/posts/43/replies.atom"
          type="application/atom+xml"
          thr:count="3"/>

    <atomic:signature xmlns:atomic="https://atomicmicro.blog/ns/1.0"
                      key-id="2024-01"
                      algorithm="ed25519"
                      value="base64-signature"/>
  </entry>
</feed>
```

**Entry IDs** are tag URIs (RFC 4151) minted from the feed's base URL: `tag:<host>,<YYYY-MM>:<path>/post-<n>`, where `<path>` is the base URL's port (if any) and path, without surrounding slashes, and the `<path>/` part is dropped when both are empty. So `https://miki.one` mints `tag:miki.one,2024-12:post-42`, `https://amhost.example/mikio` mints `tag:amhost.example,2024-12:mikio/post-42`, and `http://localhost:8100` mints `tag:localhost,2024-12:8100/post-42`. Identities on the same host can't collide. Posts from the content folder use `content/<file path without extension>` instead of `post-<n>` (e.g. `tag:miki.one,2026-10:content/2026-10-27`), or the `id` from their frontmatter. IDs are assigned once and never recomputed.

### Entry Signing

Each entry is signed independently so it can be verified in isolation — aggregators can add metadata, rewrite links, reorder, or filter entries, but the signed core is tamper-evident.

The signature covers a fixed list of fields, not the XML, so it survives any re-serialization of the entry (whitespace, attribute order, namespace prefixes).

**Signing input:** one `name: value` line per field, in this order, joined by `\n`:

```
id: tag:miki.one,2024-12:post-42
author: https://miki.one/
published: 2024-12-01T12:00:00Z
updated: 2024-12-01T12:00:00Z
in-reply-to: tag:other.example,2024-12:post-99
verb: 
title: Just discovered RFC 4685 has threading built in
summary: 
content-type: html
content: <p>Just discovered RFC 4685 has threading built in. …</p>
enclosure: https://miki.one/media/capybara.jpg
```

Rules:
- All lines from `id` to `content` are always present; a missing field has an empty value.
- `enclosure` lines come last, zero or more, in document order.
- Values are the text as parsed from XML (entities unescaped), not trimmed.
- Values are escaped: `\` → `\\`, newline → `\n`, carriage return → `\r`.
- `author` is the author `uri`. It binds the entry to the identity whose key verifies it. Signed entries MUST carry their own `author` element (not inherited from the feed), so they stay verifiable in isolation.
- `in-reply-to` is the `ref` attribute.
- `content-type` MUST be `text` or `html`. Signed entries cannot use `xhtml` content.

Anything not in this list (links, aggregator metadata, WebMention endpoints) is not covered by the signature.

**Signing process:**
1. Build the signing input from the entry
2. Sign it (UTF-8 bytes) with the author's Ed25519 private key
3. Embed the signature as `atomic:signature` on the entry

**Verification:** Anyone can fetch the author's public key from `<author URI>/atomic-keys` and verify. Aggregators verify on ingest (see [Ingest and Verification](#ingest-and-verification)).

### API

#### `GET /feed.atom`

Returns the Atom feed. Standard HTTP caching (`ETag`, `304 Not Modified`).

Entries are ordered by `updated`, most recent first (ties broken by post number). Edits and deletes bump `updated`, so they move to the top and reach subscribers on their next poll, without crawling history.

The feed is paged per [RFC 5005](https://tools.ietf.org/html/rfc5005) (paged feed): each page links `rel="first"` and, if there are older entries, `rel="next"`. The page cursor is `?before=<updated>,<n>` — the `updated` timestamp and post number of the last entry on the previous page, e.g. `/feed.atom?before=2026-10-21T21:07:11Z,3`. Pages are for backfilling history; subscribers polling for changes only need the first page.

#### `GET /posts/{id}`

Returns a single entry as HTML (browser) or Atom (Accept header).

#### `GET /posts/{id}/replies.atom`

Returns a feed of replies to the given entry (RFC 4685 reply feed), built from the WebMentions the feed server received. Since aggregators forward replies to the source, this is the closest thing to a complete reply list across all islands.

#### `POST /webmention`

WebMention endpoint for the feed server's own entries. Advertised via `<link rel="webmention">` on entry pages. See [Spam Control](#spam-control).

#### `POST /websub`

WebSub hub for the feed (see [WebSub](#websub)). Advertised via `<link rel="hub">` in the feed.

#### `GET /api/moderation?id={entry-id}` / `POST /api/moderation`

Moderating replies to the owner's entries in `replies.atom`. Token-authenticated. Same API as the aggregator's (see [Moderation](#moderation-v0)).

#### `POST /api/entries`

Create a new entry. Token-authenticated.

```
POST /api/entries HTTP/1.1
Host: miki.one
Authorization: Bearer <token>
Content-Type: application/json

{
  "content": "Just discovered RFC 4685 has threading built in",
  "content_type": "text",
  "in_reply_to": {
    "ref": "tag:other.example,2024-12:post-99",
    "href": "https://other.example/posts/99"
  }
}
```

Response:

```
HTTP/1.1 201 Created
Location: https://miki.one/posts/42

{
  "id": "tag:miki.one,2024-12:post-42",
  "url": "https://miki.one/posts/42",
  "published": "2024-12-01T12:00:00Z"
}
```

#### `DELETE /api/entries/{n}`

Soft-delete post number `n` (as in `/posts/{n}`): content and title are cleared, the ID is kept, `updated` is bumped and the entry is re-signed. The tombstone stays in the feed so aggregators learn about the deletion. Posts from the content folder answer `409 Conflict`: delete the file instead.

#### `GET /api/entries`

List entries in feed order, paged with the same `?before=` cursor as the feed; the response carries the next cursor as `next`. `?limit=` sets the page size (max 100). Token-authenticated. For management UIs.

#### `PUT /api/challenges/{token}`

Publish a login challenge (see [Registration and Login](#registration-and-login)). The request body is the challenge value as `text/plain` (at most 256 bytes, printable ASCII); amfeed stores it as is and serves it at `GET /atomic-challenge/{token}`, relative to the author URI like `atomic-keys`. Challenges expire after 10 minutes. Token-authenticated. `{token}` is limited to base64url characters.

#### `GET /atomic-challenge/{token}`

The challenge value as `text/plain`, or 404 if unknown or expired. Public.

#### `PUT /api/following` / `DELETE /api/following?aggregator={uri}`

Publish (or stop publishing) the following list for one aggregator (see [Shared Following Lists](#shared-following-lists)). The body is JSON, and amfeed renders it as OPML at `/following/<aggregator>.opml`. Token-authenticated.

```json
{
  "aggregator": "https://capyclub.example/",
  "feeds": [
    {"url": "https://alice.example/feed.atom", "title": "Alice", "html_url": "https://alice.example/"}
  ]
}
```

#### `GET /following/{aggregator}.opml`

The list for one aggregator, as `text/x-opml`. Public; 404 when not shared.

### Authentication

**Bearer tokens only.** No OAuth, no PKCE, no refresh tokens, no scopes.

Generate a token, store its hash, use it in the `Authorization` header. The first (admin) token is created locally by `amfeed init`; admin tokens can create further posting tokens via the API. The feed server belongs to its single author, so a token only has to prove "this is the owner".

```
POST /api/tokens HTTP/1.1
Authorization: Bearer <admin-token>

{"name": "my-phone", "expires": "2025-12-01T00:00:00Z"}
```

### WebSub

amfeed is its own [WebSub](https://www.w3.org/TR/websub/) hub. The feed advertises it with `<link rel="hub" href="<base URL>/websub">` next to its `rel="self"` link.

- **Subscribing:** subscribers (aggregators) send `POST /websub` with `hub.mode=subscribe`, `hub.topic` (the feed URL), `hub.callback`, and optionally `hub.lease_seconds` and `hub.secret`. amfeed verifies the intent by calling the callback with a challenge, as WebSub requires. The feed's own URL is the only topic.
- **Publishing:** when an entry is created, edited or deleted, amfeed POSTs the first page of the feed to every subscriber's callback, signed with `X-Hub-Signature` when the subscriber gave a secret. Subscribers ingest it like a polled page.
- **Delivery** is best-effort, with a few retries. Subscriptions expire with their lease, and subscribers renew them.

Other feed servers may advertise any hub; aggregators subscribe to whichever hub a feed names. Push complements polling and doesn't replace it (see [Polling](#polling)).

### Sending WebMentions

When an entry has a `thr:in-reply-to` `href`, amfeed sends a WebMention with `source` set to the entry's permalink and `target` set to that `href`, to the endpoint discovered at the target (HTTP `Link` header or `<link rel="webmention">`). Likes and reposts do the same with their target. When the entry is edited or deleted, amfeed sends it again, so the receiver re-fetches the source.

- Sending happens in the background after the API call returns; failures are retried a few times, then dropped.
- On `429 Too Many Requests`, amfeed retries after `Retry-After`.
- A reply made from an aggregator's timeline points at that aggregator's post page, so the aggregator receives the reply at once. A reply to a plain blog reaches the blog directly, with no aggregator involved.
- Aggregators that ingest the reply still forward it to the source (see [Conversations and Replies](#conversations-and-replies)). WebMention is idempotent per source and target, so the duplicate is harmless.

### Content Folder

amfeed can publish a folder of Markdown files, such as a folder in a notes vault that an agent writes to (`[content]` in the config). Files are the source of the content; the database keeps everything else (post numbers, signatures, subscriptions, mentions). Posts from files and posts from the API live side by side in one feed.

- **Sync:** amfeed checks the folder at startup and then every minute. A new file becomes a post, a changed file updates its post (new `updated`, re-signed), a removed file turns its post into a tombstone, and a file that comes back revives it. Subscribers get one push per sync that changed anything. A missing folder is an error and changes nothing.
- **Which files:** those matching `pattern` (default `*.md`) relative to the folder, except hidden ones. Renaming a file makes it a new post.
- **Frontmatter:** an optional leading `---` block. amfeed reads `title`, `date` (`YYYY-MM-DD` for midnight UTC, or RFC 3339) and `id`, as plain or quoted one-line values, and ignores every other key. A file whose frontmatter can't be read is skipped and logged.
- **Title:** `title` from the frontmatter, else a leading `# Heading` (removed from the body), else the file name without extension.
- **Published:** `date` from the frontmatter, else when amfeed first saw the file.
- **Content:** Markdown (CommonMark with GitHub extensions) rendered to HTML, which is what's signed. Raw HTML in the file is left out. A wikilink (`[[name]]`, `[[name|label]]`) becomes a link when another file in the folder has that name, otherwise plain text, so nothing points into unpublished notes.
- **Ownership:** the sync owns posts from files; the API doesn't change or delete them.

### Storage

The protocol doesn't prescribe storage. The reference implementation uses:
- SQLite (WAL mode) for entries and token hashes
- PEM files for signing keys
- Feeds and pages rendered per request

A feed server could be as simple as a static file generator that writes `.atom` files to disk.

---

## 5. amagg — Aggregator

The aggregator is where the social happens. It holds the follow graph, merges feeds into timelines, and mediates interactions. Think of it as a "room" — a space where conversations happen, with its own community norms and moderation.

### Responsibilities

1. Maintain user accounts and follow graphs
2. Subscribe to and poll/receive Atom feeds — always including its members' own feeds
3. Merge subscribed feeds into per-user timelines
4. Serve merged timelines as Atom feeds
5. Thread replies from its members' feeds into conversations
6. Act as WebMention endpoint for conversations in its space
7. Forward replies to their source via WebMention
8. Verify entry signatures at ingest, and mark entries verified or unverified
9. Proxy feed fetches for clients
10. Moderate content within its space

### The Island Model

**Aggregators are islands by default.** A post published to a feed server is just sitting there — it doesn't travel anywhere until an aggregator pulls it. And interactions within an aggregator (replies, likes) stay within that aggregator unless explicitly sent outward via WebMention.

This is a feature, not a limitation:
- No blast radius — a viral post doesn't replicate to every server on the network
- Aggregators can have different community standards without imposing them on others
- Cross-posting is an explicit, conscious action
- Spam containment is natural — spam only reaches aggregators that subscribe to the spam feed

#### What's in an Island

An aggregator is both a social space and a feed reader. The two are separated by role, not by feed type:

- **Members are the island.** Members log in (see [Registration and Login](#registration-and-login)), and their feeds make up the shared space: the island timeline, threads, and what the island serves and forwards.
- **Follows are personal.** A member can follow any Atom feed: another person's amfeed, a blog, a release feed. Followed entries show up in that member's timeline only. Following a feed doesn't make it part of the island; other members don't see it unless they follow it too.
- **Reposts and replies bridge the two.** When a member reposts or replies to a followed entry, their own (island) entry brings it into the island's view and threads.

There's one stream, no separate reader view and no read/unread state. A feed's individual posts are on its profile page.

### Data Model

#### User Account

```go
type User struct {
    ID          string    // aggregator-local user ID
    AuthorURI   string    // the identity: author URI, proven at login
    FeedURL     string    // discovered from the profile page at AuthorURI
    DisplayName string    // cached from feed
    AvatarURL   string    // cached from feed
    JoinedAt    time.Time
}
```

#### Follow Relationship

```go
type Follow struct {
    FollowerID string    // local user ID
    FeedURL    string    // URL of the feed being followed
    CreatedAt  time.Time
}
```

#### Merged Entry

```go
type MergedEntry struct {
    EntryID       string    // original entry ID
    AuthorURI     string    // claimed author; proven only if Verified
    FetchedFrom   string    // URL amagg fetched the entry from (feed, WebMention source, …)
    InReplyToRef  string    // thr:in-reply-to ref, used for threading
    SourceContent []byte    // original Atom entry, as received
    ReceivedAt    time.Time
    Verified      bool      // signature checked against AuthorURI's key
    // Aggregator-local metadata (not part of signed content)
    LocalReplies  int
    LocalLikes    int
    Moderated     bool
}
```

### Registration and Login

You prove you own an identity the way ACME's HTTP-01 challenge proves you own a domain: the aggregator hands you a token, and you show you can write under your identity URL by placing it there. This uses the same rule as the signing keys: whoever controls the identity URL owns the identity. Nothing is posted to the feed.

Registration and login are the same flow. The first successful check creates the account; later checks log in, e.g. from a new device.

People find aggregators by word of mouth. There is no directory and no discovery protocol.

Who may register is configured per aggregator (`[aggregator] registration`):

- **`invite`** (default): the first login must carry an invite code (`"invite": "…"` in `POST /api/login`). The operator creates single-use codes with the `amagg` command line tool.
- **`open`**: anyone who passes the challenge becomes a member.

```
amfront                      amagg (agg.example)                 amfeed (miki.one)
   │ POST /api/login               │                                    │
   │ {author_uri}  ───────────────►│                                    │
   │ ◄──────────── {token}         │                                    │
   │ PUT /api/challenges/{token}   │                                    │
   │ body: {token}.https://agg.example ───────────────────────────────►│
   │ POST /api/login/verify        │                                    │
   │ {token}  ────────────────────►│ GET /atomic-challenge/{token} ───►│
   │                               │ ◄──── {token}.https://agg.example  │
   │ ◄──────────── {session token} │                                    │
```

1. The client asks for a challenge: `POST /api/login {"author_uri": "https://miki.one/"}`. The aggregator returns a random token (32 bytes, base64url), valid for 10 minutes.
2. The client publishes the challenge value `<token>.<aggregator origin>` at `<author URI>/atomic-challenge/<token>`. With amfeed, that's `PUT /api/challenges/<token>` with the posting token; anyone else can upload a static file.
3. The client asks the aggregator to check: `POST /api/login/verify {"token": "…"}`. The aggregator fetches the challenge URL and compares the body, trimmed of surrounding whitespace, with the expected value.
4. On a match, the aggregator discovers the feed from the profile page at the author URI (`<link rel="alternate" type="application/atom+xml">`), creates or finds the account, and returns a session token (bearer, like everywhere else).

Rules:

- **The body must match, not just exist.** Many sites answer 200 for any path (catch-all pages, single-page apps); the comparison is what proves the write.
- **The aggregator origin in the value** binds the challenge to the aggregator that issued it. Otherwise a malicious aggregator E could request a token from aggregator A for your identity, pass it off as its own challenge when you log into E, and take over your account on A. The client knows which aggregator it is talking to and writes that origin.
- **No redirects** are followed when fetching the challenge, so the proof stays under the identity URL. HTTPS is required, except for loopback addresses during development.
- Tokens are single-use and expire after 10 minutes; amfeed deletes published challenges after 10 minutes as well.
- The feed discovered at the author URI must carry entries whose `author` is that URI; that's what ties the account to the posts the aggregator will ingest.
- On shared hosts, the host must ensure tenants can't write under each other's paths, the same requirement as for `atomic-keys`.

The posting token never leaves the client, and amfeed never signs anything on an aggregator's behalf.

### Authentication and Access

Members have no passwords; the login flow above is the only way to prove who you are. What it yields are tokens, used in two ways:

- **Bearer tokens** for amfront and other clients (`Authorization: Bearer …`), as everywhere else.
- **Feed reader credentials** for plain feed readers, which can't run the login flow or send custom headers but do support HTTP Basic auth. A member creates one per reader in amfront (`POST /api/readers`, bearer-authenticated); the aggregator generates a random password and shows it once. The reader then uses `https://capyclub.example/timeline/miki.atom` with username `miki` (the member name) and that password. Basic auth is accepted only on aggregators served over HTTPS (loopback excepted), only on feeds (`*.atom`), and the passwords are stored hashed and revocable, like tokens. Feeds that need credentials answer `401` with both a `Bearer` and a `Basic` challenge.

Basic auth doesn't weaken the CORS rule: browsers attach cached Basic credentials to cross-origin requests only with `credentials: "include"`, which `Access-Control-Allow-Origin: *` forbids. The aggregator never sends `Access-Control-Allow-Credentials`.

**Public or private island.** Configurable per aggregator (`[aggregator] public`):

- **Public** (default): the island timeline, post pages and thread feeds are readable by anyone, including other islands.
- **Private**: these require member credentials. Other islands can't consume the island, and replies from outside it can't find its post pages. Replies by members still reach their sources via WebMention, since a member's reply is public in their own feed anyway.

Member timelines always require that member's credentials.

### Following

```
POST /api/follow HTTP/1.1
Authorization: Bearer <aggregator-token>

{
  "feed_url": "https://other.example/feed.atom"
}
```

Any Atom feed can be followed (RSS 2.0 is not supported). The aggregator adds the feed to its subscription list and polls it once, however many members follow it; members' own feeds are always on the list. If the feed advertises a WebSub hub, the aggregator subscribes for push updates. Otherwise, it polls (see [Polling](#polling)).

Entries from plain feeds are ordinary unverified entries (see [Ingest and Verification](#ingest-and-verification)). When an entry has no author URI, the feed's author is used, and failing that the feed URL.

### Ingest and Verification

Entries reach an aggregator by many paths: polling member and followed feeds, WebMention (amagg fetches the `source`), other aggregators' timelines, WebSub pushes, fetching a missing parent. Many of these relay entries by authors other than whoever serves them, and replies often come from plain blogs that don't sign at all.

Every path is handled the same way: **accept the entry, verify if possible, and record the result.**

- **Verified:** the entry carries an `atomic:signature` that verifies against the key document at its `author` URI. Keys are cached per author and re-fetched when an unknown `key-id` appears, so verification happens once, at ingest.
- **Unverified:** everything else: unsigned, a failed signature, an unreachable key document. The claimed author is only a claim. The aggregator records the URL it fetched the entry from (`FetchedFrom`), which can't be faked because the aggregator fetched it itself.

Frontends show this distinction: a verified entry shows its author; an unverified one shows where it came from ("unverified · from bob.blog"). For a plain blog replying via WebMention, "from bob.blog" is true even without cryptography.

**Replacement rule.** Entries are unique by `id`. A newer version (edit, tombstone, or just another copy) replaces the stored entry only if it is at least as trustworthy:

| Stored | Incoming | Replaces if |
|---|---|---|
| unverified | verified | same `author` (the author started signing) |
| verified | verified | same `author` |
| unverified | unverified | same `FetchedFrom` (a plain blog editing its own post) |
| verified | unverified | never |

This also settles which entry a reply's `thr:in-reply-to ref` points to: once an ID is verified, nobody else can take it over.

**Missing parents.** When a reply's parent is unknown, the aggregator fetches it once from the `in-reply-to` `href` (asking for Atom) and ingests it like any other entry. If that fails, the reply is shown without its parent.

#### Polling

- Conditional requests (`ETag`, `Last-Modified`), so unchanged feeds cost a `304`.
- Paged feeds (RFC 5005) are followed via `rel="next"` until an entry with an `updated` older than the last one seen. Entries with `updated` equal to the last seen value are re-checked, since timestamps have second precision.
- The last seen `updated` never advances past the aggregator's current time, so an entry dated in the future can't hide later updates.
- The first fetch of a feed reads at most a few pages; history beyond that is not backfilled.
- The interval adapts per feed between `min_interval` and `max_interval`: shorter for feeds that change often, backing off for quiet or failing ones.
- Feeds with a WebSub subscription are still polled, at `max_interval`, so a lost push delays an entry but doesn't lose it.

### Timeline Generation

There are two kinds of timeline, both served as Atom feeds:

- **Island timeline:** entries from all members' feeds (like Mastodon's local feed). This is what other islands and non-members can read.
- **Member timeline:** per member, the island's entries plus the feeds that member follows, personalized.

```
GET /timeline.atom          ← island
GET /timeline/miki.atom     ← member
```

The reference order is chronological by `published`, newest first, so edits update an entry in place without moving it. Timelines are paged like feeds (RFC 5005, `?before=` cursor).

The aggregator MAY apply any ordering algorithm — chronological, ranked, filtered — because it's just ordering Atom entries. Different aggregators can compete on timeline quality, just as podcast apps compete on discovery and recommendations. One obvious example is balancing high-volume feeds against low-volume ones: limit how much of the timeline one feed takes up, or collapse bursts ("12 new posts from Hacker News").

#### Re-served Entries

When the aggregator serves an entry (in a timeline or on a post page), it:

- sets `link rel="alternate"` to its **own** URL for the post, so replies made from here point here
- adds `link rel="via"` pointing to the URL it got the entry from
- keeps `atom:source` with the original feed's metadata (`id`, `title`, `link rel="self"`)
- sets `thr:total` (RFC 4685) to the number of direct replies it has stored, replacing any upstream value
- advertises its own WebMention endpoint on the post page

None of these are signed fields, so the signature stays valid.

```xml
<entry>
  <id>tag:miki.one,2024-12:post-42</id>
  <link rel="alternate" href="https://capyclub.example/posts/abc123"/>
  <link rel="via" href="https://miki.one/posts/42"/>
  <source>
    <id>https://miki.one/feed.atom</id>
    <title>Mikio's Feed</title>
    <link rel="self" href="https://miki.one/feed.atom"/>
  </source>
  <thr:total>2</thr:total>
  <!-- original content, thr:in-reply-to, atomic:signature unchanged -->
</entry>
```

### Conversations and Replies

A reply is just an Atom entry on the replier's feed server, with `thr:in-reply-to`. Threads are matched by `ref` (the Atom entry `id`), never by `href`, since every aggregator shows the same post under its own URL.

Threads are per island: a thread shows the replies this aggregator has. Neither aggregators nor frontends pull in the source's `replies.atom`.

#### Inside an Island

The aggregator ingests its members' feeds (pushed via WebSub, or polled), sees the reply's `thr:in-reply-to ref`, and attaches it to the thread. A reply made from the island's timeline points at the island's post page, so the member's feed server also sends the island a WebMention for it (see [Sending WebMentions](#sending-webmentions)). Whichever arrives first wins; the other is a duplicate.

#### Forwarding to the Source

When the aggregator ingests a member's reply, it sends a WebMention to the parent post's `via` URL — the author's own post URL if the aggregator got the entry directly from the author's feed. This notifies the author's feed server (or blog) — even if the author is not on this aggregator. The `source` is the reply's own URL on the replier's feed server, not the aggregator's copy, so forwarding works from private islands too.

The source is where islands meet. Its `replies.atom` collects replies forwarded from every island:

```
        island A (agg-a)                      island B (agg-b)
  Bob replies in his feed                 Carol replies in her feed
          │ agg-a ingests                         │ agg-b ingests
          ▼                                       ▼
   thread on agg-a                         thread on agg-b
          │ WebMention                            │ WebMention
          └──────────────►  alice.example  ◄──────┘
                         (amfeed: /webmention)
                         /posts/42/replies.atom   ← all replies, all islands
```

`replies.atom` is for the author, not for islands: it is the author's view of every reply their post received.

#### Replies from Outside the Island

Alice is on island A. Bob reads A's public timeline but is a member of island B, so A does not ingest Bob's feed. Bob's reply points at A's URL (the `alternate` A served), so:

1. Bob's reply lands in his feed with `thr:in-reply-to href="https://agg-a.example/posts/abc123"`
2. Island B ingests it and sends a WebMention to the endpoint discovered at that `href` — island A's
3. Island A receives `source=https://bob.example/posts/7&target=https://agg-a.example/posts/abc123`
4. Island A fetches the source, verifies it links to the target, verifies the signature
5. Island A adds the reply to the thread and forwards it to Alice's feed server

If Bob had replied to Alice's original URL instead, the reply would go to Alice's feed server only, and island A would not see it. That's consistent with islands.

**Sending rule:** whoever sends a WebMention for a reply (the replier's feed server, and every aggregator that ingests the reply) sends it to the endpoint discovered at the `in-reply-to` `href`. **Receiving rule:** an aggregator that receives a reply forwards it to the source (via `via`). WebMention is idempotent per source+target, so repeated deliveries are harmless.

#### Islands Consuming Islands

An aggregator's timeline is an Atom feed, so island B can subscribe to island A. B removes duplicates by entry `id` and verifies signatures against the `author` key, independent of which feed the entry arrived through. B sets its own `alternate` and points `via` at A. Replies on B then forward along the `via` chain: B → A → Alice.

#### Spam Control

WebMention endpoints (amfeed and amagg) accept any standard WebMention; senders need nothing beyond the WebMention spec. Spam is kept down by giving it little to gain and by plain limits:

- **Source check:** as WebMention requires, the receiver fetches the `source` and accepts it only if it links to the `target`. Each mention needs a real page.
- **No link payoff:** links inside replies are rendered with `rel="nofollow ugc"`, so they carry no search ranking.
- **Little reach:** a reply shows only in the thread it was sent to, on one island or one feed server.
- **Rate limit per sending host:** over the limit, the endpoint answers `429 Too Many Requests` with `Retry-After`.
- **Blocklist:** the operator can block sending hosts.

Whatever gets through is ingested like any other entry: verified if it can be, otherwise shown as unverified with where it came from. Unwanted entries can then be moderated (see [Moderation](#moderation-v0)).

### Moderation (v0)

You can't control what others write about you, but you can choose what shows in the spaces you're part of. On an island, what members may moderate is the island's policy: one island offers nothing, another lets authors hide what's posted under their posts.

**Actions.** There are three, with fixed meanings, so every frontend and feed consumer can display them:

| Action | Effect |
|---|---|
| `fold` | The entry stays, marked sensitive. Frontends collapse it behind a "show" toggle. |
| `hide` | The entry is served as a tombstone: same `id`, content and title removed, marked as hidden. Replies to it stay. |
| `prune` | The entry and everything below it in the thread are left out of timelines and threads. |

Nothing is deleted: moderated entries stay in storage, and the action `none` undoes any of them.

**Who may do what.** The island decides, per caller and entry. Typical rules: the operator may do anything; a member may moderate entries below their own verified posts (direct replies only, or the whole subtree); nobody else may do anything. A frontend doesn't need to know the rules: it asks which actions are available and offers those.

```
GET /api/moderation?id=tag:bob.example,2026-10:post-7
Authorization: Bearer <aggregator-token>

{"actions": ["fold", "hide", "prune"], "current": null}
```

```
POST /api/moderation
Authorization: Bearer <aggregator-token>

{"id": "tag:bob.example,2026-10:post-7", "action": "hide"}
```

**In feeds.** Folded and hidden entries carry an unsigned marker, like the other metadata the aggregator adds (see [Re-served Entries](#re-served-entries)):

```xml
<atomic:moderation action="hide" by="author"/>
```

`by` is `author` (the author of a post above the entry) or `operator`. A hidden entry can't verify, since its content is gone; frontends show "hidden by the author" for it, not a signature failure. Pruned entries are simply absent.

**On a feed server.** amfeed implements the same API for replies to its own entries, as shown in `replies.atom`. Only the owner moderates there, with no restrictions: it's their space.

### API

#### `GET /timeline.atom`

Island timeline: entries from all members' feeds. Paged like the member timeline.

#### `GET /timeline/{user}.atom`

User's timeline (island plus their follows) as Atom feed, paged with `?before=`. `{user}` is the member's name, derived from the author URI: host and path, with `/` and `:` replaced by `_` (`https://miki.one/` → `miki.one`, `https://amhost.example/mikio/` → `amhost.example_mikio`).

#### `GET /posts/{id}`

Single post view with thread context. HTML or Atom based on Accept header.

#### `GET /posts/{id}/thread.atom`

The whole conversation the post belongs to, as an Atom feed in conversation order: from the topmost ancestor the aggregator knows, depth-first through all replies (matched by `thr:in-reply-to ref`). Includes every entry the aggregator has stored, also fetched parents and cross-aggregator replies received via WebMention. The HTML post page shows the same thread.

#### `POST /api/login` / `POST /api/login/verify`

Request a login challenge for an author URI, then have it checked; returns a session token. Registers the account on first login (see [Registration and Login](#registration-and-login)).

#### `POST /api/follow` / `DELETE /api/follow`

Follow or unfollow a feed.

#### `POST /api/readers` / `GET /api/readers` / `DELETE /api/readers/{id}`

Create or revoke a feed reader credential (HTTP Basic password) for the authenticated member; see [Authentication and Access](#authentication-and-access).

#### `GET /api/following`

List followed feeds for the authenticated user.

#### `POST /webmention`

WebMention endpoint. See [Spam Control](#spam-control).

#### `GET /api/mentions/{entry-id}`

List WebMentions received for a specific entry.

#### `GET /api/moderation?id={entry-id}` / `POST /api/moderation`

Moderation actions available to the caller for an entry, and applying one (see [Moderation](#moderation-v0)).

#### `GET /api/feed?url={feed-url}`

Proxy: fetch (and briefly cache) any Atom feed or entry on behalf of the client — profiles, reply feeds, feeds not yet followed. Token-authenticated, and only returns responses that parse as Atom (`502` otherwise), so it can't be used as an open proxy.

### Follow Graph Portability

The follow graph is a list of feed URLs, exchanged as **OPML 2.0**, the format every feed reader and podcast app already imports and exports. An aggregator SHOULD provide an export endpoint:

```
GET /api/following/export      → OPML
```

Switching aggregators = export your follow list, import it on the new one. Your posts stay at your feed URL. Your identity doesn't change.

#### Shared Following Lists

Your follow graph is also how you bootstrap on a new aggregator, so you MAY publish it under your identity, opt-in. Publishing it there (not on the aggregator) keeps it when you leave the aggregator.

Lists are kept **per aggregator**, since what you follow on a science aggregator differs from what you follow on a shitposting one, and are keyed by the aggregator's URI. The list lives at `<author URI>/following/<aggregator>.opml`, where `<aggregator>` is the aggregator URI without scheme and trailing slash, with `/` replaced by `_`: `https://capyclub.example/` → `following/capyclub.example.opml`, `https://host.example/science` → `following/host.example_science.opml`. Whoever knows the aggregator can derive the URL.

Each list is a plain OPML subscription list:

```xml
<opml version="2.0">
  <head><title>Following on capyclub.example</title><ownerId>https://miki.one/</ownerId></head>
  <body>
    <outline type="rss" text="Alice" xmlUrl="https://alice.example/feed.atom" htmlUrl="https://alice.example/"/>
  </body>
</opml>
```

(`type="rss"` is OPML's conventional value for any feed subscription, Atom included.) To seed your follows on a new aggregator, you name the aggregator whose list to copy ("seed me from capyclub.example"), and the new aggregator fetches that list.

The frontend keeps the shared lists up to date. It already holds the posting token, so when you follow or unfollow with sharing on, it does a full resync: it fetches the aggregator's export (`GET /api/following/export`) and writes it to your feed server. Follows changed elsewhere (another client, the aggregator dropping a dead feed) are picked up on the next resync. Aggregators never get write access to your identity.

### Storage

The protocol doesn't prescribe storage: a timeline is just a paged Atom feed, however it is produced. An aggregator can compute timelines on read, precompute them per user on ingest (fan-out), or mix both.

The reference implementation uses:
- SQLite (WAL mode) for members, tokens, invites, follows, feeds with their polling state, and entries (one row per entry `id`, stored as received, however many members follow its feed)
- Poll scheduling: a `next_poll` column and an in-process poller goroutine
- Login challenges in memory
- Timelines computed at query time (entries joined with members' feeds and follows, ordered), no per-user fan-out
- No retention: entries are kept indefinitely

---

## 6. amfront — Frontend

The frontend is a pure client. It holds no user data, no social graph, no content. It reads from aggregators and posts to feed servers.

### Responsibilities

1. Authenticate with the user's feed server (posting token)
2. Authenticate with the user's aggregator(s) (session token)
3. Read timeline from aggregator
4. Display entries with threading
5. Compose and publish new entries to feed server

amfront does not send WebMentions. The user's feed server sends them for the user's replies, and aggregators forward replies they ingest (see [Conversations and Replies](#conversations-and-replies)).

### Architecture

amfront is a static web application (HTML + JS). It talks to exactly two kinds of servers, both of which send CORS headers:

- **Aggregator(s)** — all reading: timelines, threads, profiles. Feeds from third-party servers are fetched through the aggregator's proxy (`GET /api/feed`), so amfront never needs CORS from arbitrary feed servers.
- **Feed server** — posting only. Posting never goes through the aggregator; otherwise the aggregator would hold the posting token and could post as the user.

The reference implementation is a Vue 3 + Vite + TypeScript app in `amfront/`. `npm run build` produces static files that any static host can serve; a small Go binary embedding them may follow. It verifies entry signatures in the browser (Web Crypto Ed25519), with the same signing input as the Go code.

For reading, a feed server is an aggregator with one member who follows nobody: both serve paged Atom feeds of signed entries. So amfront treats a timeline as just an Atom feed URL: your own `feed.atom` or an aggregator timeline.

```go
type Connections struct {
    FeedServerURL   string // user's amfeed instance
    FeedServerToken string // bearer token for posting
    Aggregators     []AggregatorConn
}

type AggregatorConn struct {
    URL   string // aggregator base URL
    Token string // aggregator session token
}
```

### Configuration

A user configures amfront with:

1. **Feed server URL + token** — where to publish posts
2. **Aggregator URL(s)** — where to read timelines. amfront obtains the session token itself by running the login flow against the user's feed server (see [Registration and Login](#registration-and-login)).

The reference implementation keeps this in the browser (localStorage). A frontend MAY keep it server-side instead; it is the only state a frontend holds.

### Pages

| Route | Description |
|-------|-------------|
| `/` | Home timeline (merged from connected aggregators) |
| `/post` | Compose new post |
| `/thread/{id}` | Thread view |
| `/profile/{feed-url}` | Profile view (fetched from feed) |
| `/settings` | Configure feed server + aggregator connections (login), feed reader passwords, following list sharing |
| `/notifications` | WebMention activity on your posts |

### Posting Flow

1. User writes a post in the compose UI
2. amfront sends `POST /api/entries` to the user's feed server. For a reply, `in_reply_to` carries the parent's `id` as `ref` and the `alternate` URL the user saw as `href`
3. Feed server creates the entry, signs it, adds it to the feed, pushes the feed to its WebSub subscribers, and for a reply sends a WebMention to the `href`
4. The aggregator ingests the entry, threads it, and forwards replies to the source

### Multiple Aggregators

Since a user can be on multiple aggregators, amfront can show a merged view or per-aggregator tabs. This is a UI decision — the protocol doesn't care.

### Alternative Frontends

Because amfront is just an Atom consumer + API client, anyone can build a replacement:
- A native iOS/Android app
- A CLI tool
- A terminal UI (TUI)
- A browser extension
- An Emacs mode (someone will)
- A bot framework

---

## 7. Entry Types

Atom's `<content>` element with `type` attribute handles basic post types. We use Atom extension elements for richer types:

### Micro Post (default)

```xml
<entry>
  <content type="html">&lt;p&gt;Short text post, like a tweet.&lt;/p&gt;</content>
</entry>
```

### Reply

```xml
<entry>
  <content type="html">&lt;p&gt;My reply text&lt;/p&gt;</content>
  <thr:in-reply-to
    ref="tag:other.example,2024:post-99"
    href="https://other.example/posts/99"/>
</entry>
```

### Like / Repost

Expressed as Atom entries with extension elements:

```xml
<entry>
  <atomic:verb xmlns:atomic="https://atomicmicro.blog/ns/1.0">like</atomic:verb>
  <thr:in-reply-to ref="tag:other.example,2024:post-99"/>
</entry>
```

```xml
<entry>
  <atomic:verb xmlns:atomic="https://atomicmicro.blog/ns/1.0">repost</atomic:verb>
  <content type="html">&lt;p&gt;Optional commentary on the repost&lt;/p&gt;</content>
  <thr:in-reply-to ref="tag:other.example,2024:post-99"/>
</entry>
```

### Media Attachments

```xml
<entry>
  <content type="html">&lt;p&gt;Look at this capybara&lt;/p&gt;</content>
  <link rel="enclosure" href="https://miki.one/media/capybara.jpg"
        type="image/jpeg" length="204800"/>
</entry>
```

### Long-Form

```xml
<entry>
  <title>Why Atom Still Matters in 2025</title>
  <summary>A defense of RSS/Atom in the age of proprietary protocols.</summary>
  <content type="html">
    &lt;article&gt;Full HTML content...&lt;/article&gt;
  </content>
</entry>
```

---

## 8. Service Configuration

Config files are TOML. Relative paths are resolved against the config file's directory; unknown keys are an error.

### amfeed

Generated by `amfeed init`:

```toml
# amfeed.toml
[server]
listen = ":8100"

[feed]
title = "Mikio's Feed"
author_name = "Mikio"
author_uri = "https://miki.one/"
base_url = "https://miki.one"

[signing]
private_key_path = "keys/2026-10.pem"
key_id = "2026-10"
key_created = 2026-10-21T21:00:00Z

[storage]
path = "amfeed.db"

[webmention]
per_host_per_hour = 30    # incoming mentions per sending host, then 429
block_hosts = []          # sending hosts whose mentions are rejected

# Optional: publish Markdown files as posts (see Content Folder)
[content]
dir = "/home/mikio/vault/briefs"
pattern = "*.md"          # default
```

### amagg

```toml
# amagg.toml
[server]
listen = ":8200"

[aggregator]
name = "Capy Club"
base_url = "https://capyclub.example"
public = true             # island timeline, post pages and threads readable without login
registration = "invite"   # "invite" or "open"

[polling]
default_interval = "5m"
min_interval = "1m"
max_interval = "1h"

[webmention]
per_host_per_hour = 30    # incoming mentions per sending host, then 429
block_hosts = []          # sending hosts whose mentions are rejected

[storage]
path = "amagg.db"
```

### amfront

```toml
# amfront.toml — only for the reference static file server
[server]
listen = ":8300"
```

---

## 9. Deployment Topology

### Self-Hosted (all three)

```
You run: amfeed + amagg + amfront
         miki.one → amfeed (your identity + content)
         club.miki.one → amagg (your aggregator)
         app.miki.one → amfront (your UI)
```

### Mixed (common case)

```
You run:    amfeed at miki.one (your identity + content)
Someone runs: amagg at capyclub.example (aggregator you joined)
You use:    amfront at atomicmicro.blog (hosted frontend)
```

### Minimal

```
You run:    amfeed (even a static file server works)
You join:   someone's aggregator
You use:    any frontend
```

### Aggregator-Only

```
You run:    amagg for your community
Others:     bring their own feeds and frontends
```

---

## 10. Comparison with Existing Protocols

| Feature | ActivityPub | AT Protocol | Nostr | OStatus | **Atomic Micro Blogging** |
|---------|-------------|-------------|-------|---------|--------------------------|
| Identity | WebFinger + Actor URL | DID + handle | keypair (npub) | WebFinger | **URL (your domain)** |
| Content format | JSON-LD Activity | DAG-CBOR | JSON events | Atom + ActivityStreams | **Atom (RFC 4287)** |
| Federation | Server-to-server push | Relay + AppView | Relay mesh | PuSH + Salmon | **WebSub + WebMention** |
| Social graph | Per-instance | PDS + AppView | Per-relay/client | Per-instance | **Per-aggregator** |
| Aggregation | Baked into server | AppView (Bluesky) | Client-side | Baked into server | **Independent layer** |
| Key innovation | W3C standard, wide adoption | Portable identity, global view | Censorship resistance | First Atom-social stack | **Separable aggregation** |
| Custom protocol | ActivityStreams vocabulary | AT Protocol (custom) | NIP system (custom) | Salmon (custom) | **Small Atom extensions (`atomic:` namespace)** |

---

## 11. Open Questions

1. **Follow graph protection** — Signatures keep an aggregator from forging posts, and your content lives on your feed server, but the aggregator sees your follow graph and reading patterns, and shared following lists publish the graph. How much protection does the follow graph need, e.g. lists readable only by aggregators you name?

### Deferred

- **Rich media** — Media hosting is the feed server's problem. Should amfeed include media upload, or delegate to external hosts?
- **Entry size limits** — Should feeds enforce a max entry size? Micro-blogging suggests yes (keep it short), but the protocol shouldn't prevent long-form.
- **Likes in feed readers** — Likes and reposts are entries, so they show up as near-empty items in plain feed readers. Should they live in a separate feed?
- **Hosting for people without servers** — amfeed is single-author. A separate multi-tenant host (working name *amhost*) would serve identities like `https://amhost.example/mikio/` (the protocol supports identities with a path). Open: account management (possibly a `forward_auth` proxy passing trusted user headers to a localhost-bound backend), and whether the host holds signing keys (custodial) or users bring their own.

- **Posting from other sites** — A site that isn't your client (e.g. a blog's comment box) can't post to your feed today: you reply from your own client, or the site links to it with the reply prefilled. Two ways to reply without leaving the site; your entries stay on your feed server and are signed there in both:
  - **Embedded compose box:** you enter your identity URL on the site; the site discovers your compose page from your profile (a rel link) and embeds it in an iframe, passing only `in-reply-to`. You log in and post inside the iframe, which belongs to your feed server; it reports its height and the posted URL to the site via `postMessage`. The site never holds a token. Open: browsers partition iframe storage per embedding site, so you log in once per site (passkeys work in cross-origin iframes); clickjacking, since the compose page must be embeddable everywhere (text typed only inside the iframe, the reply target shown, a real click to post).
  - **[IndieAuth](https://indieauth.spec.indieweb.org/):** you approve the site at your own feed server, and it gets a narrow token (replies only, possibly only to that site, expiring) to post to your feed from its own UI. IndieAuth also proves ownership of an identity URL, so it could stand in for the aggregator login challenge.

  Both need a browser login for the owner at amfeed, which it doesn't have.

### Out of Scope

- **Direct messages.**

---

## 12. Implementation Roadmap

### Phase 1: Foundation

- [x] `amfeed`: Atom feed generation, entry CRUD, token auth, SQLite storage
- [x] `amfeed`: Entry signing (Ed25519, field-based)
- [x] `amfeed`: Paged feed (RFC 5005), ordered by `updated`
- [x] `am`: CLI client — post, reply, list, delete, tokens, show with signature verification
- [x] `amfront`: Basic static web UI — read timeline, compose posts, reply, delete, in-browser signature verification
- [x] Identities with a path: keys at `<author URI>/atomic-keys`, path in tag IDs

### Phase 2: amagg v1

- [x] `amfeed`: Login challenges (`PUT /api/challenges`, `GET /atomic-challenge`)
- [x] `amagg`: Login/registration via challenge, invite codes, bearer tokens
- [x] `amagg`: Follows, polling, ingest with verification and the replacement rule, SQLite storage, OPML export
- [x] `amagg`: Island and member timelines, re-served entries (`alternate`, `via`, `atom:source`), post pages
- [x] `am`: login, follow, unfollow, following, timeline
- [x] `amagg`: In-island threading, fetching missing parents
- [x] `amagg`: Feed reader credentials (HTTP Basic), public/private island
- [x] `amagg`: Feed proxy
- [x] `amfront`: Aggregator login, timeline selection, thread view, feed reader passwords, following
- [x] Local test network (`scripts/dev.sh`, `Makefile`)

### Phase 3: Interactions

- [x] `amfeed` + `amagg`: WebMention endpoint (incoming), reply feeds
- [x] `amfeed`: Sending WebMentions for replies
- [x] `amagg`: WebMention forwarding to source
- [x] `amfeed` + `amagg`: Spam control (source check, `nofollow ugc`, per-host rate limit, host blocklist)
- [ ] `amagg`: `GET /api/mentions/{entry-id}`
- [ ] `amfront`: Like/repost actions
- [ ] `amagg`: Islands consuming islands

### Phase 4: Real-Time

- [x] `amfeed`: Its own WebSub hub (subscriptions, intent verification, push on create/edit/delete)
- [x] `amagg`: WebSub subscriptions (subscribe, renew, ingest pushes, slow polling as fallback)
- [ ] `amfront`: Live timeline updates (SSE or WebSocket from amagg)
- [ ] `amfeed`: Push only what's new per subscriber: a cursor per subscription (last entry delivered), each push carries the entries after it (a page with `rel="next"` if there are more), so a push stays one entry and a failed delivery is caught up on the next one

### Phase 5: Polish

- [ ] `amagg` + `amfeed` + `amfront`: Moderation v0 (`fold`, `hide`, `prune`; island policy; markers in feeds)
- [ ] `amagg` + `amfeed` + `amfront`: Follow graph export/import, shared following lists
- [ ] `amfront`: Multi-aggregator support
- [ ] `amfront`: Media upload flow
- [ ] `amfeed`: Store the author URI per entry, so changing `base_url`/`author_uri` doesn't silently break existing signatures
- [ ] `amfeed`: Explicit tombstone marker (e.g. RFC 6721) instead of "empty content"
- [x] `amfeed`: Content folder (Markdown files with frontmatter as posts)
- [x] Landing page at atomicmicro.blog, with go-import tag for the module path
- [ ] Documentation site at atomicmicro.blog
