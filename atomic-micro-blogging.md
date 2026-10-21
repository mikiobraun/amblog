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
- **Proof-of-work challenge** — Spam control on incoming WebMentions
- **Verbs** — `atomic:verb` for likes and reposts
- **Per-aggregator feeds** — Contextual identity across aggregators

### Interoperability Goals

Interop with other social protocols (ActivityPub, OStatus, AT Protocol) is a non-goal. Interop that does matter:

- **Feed readers** — every feed is a valid Atom feed, readable by any feed reader.
- **WebMention-enabled blogs** — replies to blog posts are delivered via standard WebMention, and blogs can send WebMentions to us (subject to spam control, see Open Questions).

### CORS

Feed servers and aggregators SHOULD send `Access-Control-Allow-Origin: *` on feeds and APIs. This is safe because authentication is always via bearer tokens, never cookies, so browsers never attach credentials automatically.

---

## 3. Identity Model

**Identity = URL.** Your feed URL *is* your identity. No handles, no DID documents, no WebFinger indirection.

```
https://miki.one/feed.atom        ← identity URL, serves your Atom feed
https://miki.one/                 ← profile page (human-readable)
https://miki.one/.well-known/atomic-keys  ← signing public key(s)
```

### Profile Page

The profile page at the identity URL root is a simple HTML page that:
- Displays author info (name, bio, avatar)
- Links to the Atom feed (`<link rel="alternate" type="application/atom+xml">`)
- Optionally lists aggregators the user participates in
- Links to the signing key endpoint

### Signing Keys

Published at `/.well-known/atomic-keys` as a JSON document:

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
5. Receive WebMentions for its entries and serve them as reply feeds
6. Announce updates via WebSub (optional)

### Data Model

An entry is an Atom entry (RFC 4287) with extensions:

```xml
<?xml version="1.0" encoding="utf-8"?>
<feed xmlns="http://www.w3.org/2005/Atom"
      xmlns:thr="http://purl.org/syndication/thread/1.0">
  <title>Mikio's Feed</title>
  <link href="https://miki.one/feed.atom" rel="self"/>
  <link href="https://miki.one/" rel="alternate"/>
  <link href="https://websub-hub.example/" rel="hub"/>
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

### Entry Signing

Each entry is signed independently so it can be verified in isolation — aggregators can add metadata, rewrite links, reorder, or filter entries, but the signed core is tamper-evident.

We sign a fixed list of fields, not the XML itself. Signing XML requires canonicalization (turning XML into one exact byte sequence despite differences in whitespace, attribute order and namespace prefixes), which is complex and error-prone.

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

**Verification:** Anyone can fetch the author's public key from `/.well-known/atomic-keys` under the `author` URI and verify. Aggregators SHOULD verify on ingest and MAY flag unverified entries.

### API

#### `GET /feed.atom`

Returns the Atom feed. Standard HTTP caching (`ETag`, `Last-Modified`, `304 Not Modified`).

#### `GET /posts/{id}`

Returns a single entry as HTML (browser) or Atom (Accept header).

#### `GET /posts/{id}/replies.atom`

Returns a feed of replies to the given entry (RFC 4685 reply feed), built from the WebMentions the feed server received. Since aggregators forward replies to the source, this is the closest thing to a complete reply list across all islands.

#### `POST /webmention`

WebMention endpoint for the feed server's own entries. Advertised via `<link rel="webmention">` on entry pages. Requires a proof-of-work challenge (see [Spam Control](#spam-control-proof-of-work-challenge)).

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

Soft-delete post number `n` (as in `/posts/{n}`): content and title are cleared, the ID is kept, `updated` is bumped and the entry is re-signed. The tombstone stays in the feed so aggregators learn about the deletion.

#### `GET /api/entries`

List entries, paginated. Token-authenticated. For management UIs.

### Authentication

**Bearer tokens only.** No OAuth, no PKCE, no refresh tokens, no scopes.

Generate a token, store its hash, use it in the `Authorization` header. The first (admin) token is created locally by `amfeed init`; admin tokens can create further posting tokens via the API. The feed server is your server — this is not a third-party authorization problem, it's a "prove you're the owner" problem, and a token solves it.

```
POST /api/tokens HTTP/1.1
Authorization: Bearer <admin-token>

{"name": "my-phone", "expires": "2025-12-01T00:00:00Z"}
```

### WebSub Integration

The feed advertises a WebSub hub via `<link rel="hub">` in the feed. When a new entry is published, the feed server pings the hub. Aggregators that have subscribed to the hub receive the update in real time instead of polling.

WebSub is orthogonal — the system works fine with plain polling, and WebSub can be added to any existing feed server without protocol changes.

### Storage

amfeed is deliberately storage-agnostic. Reference implementation uses:
- SQLite for entry metadata and full-text search
- Filesystem for generated Atom XML (cacheable, servable by any static file server or CDN)
- Flat files for signing keys

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
8. Verify entry signatures
9. Proxy feed fetches for clients
10. Moderate content within its space

### The Island Model

**Aggregators are islands by default.** A post published to a feed server is just sitting there — it doesn't travel anywhere until an aggregator pulls it. And interactions within an aggregator (replies, likes) stay within that aggregator unless explicitly sent outward via WebMention.

This is a feature, not a limitation:
- No blast radius — a viral post doesn't replicate to every server on the network
- Aggregators can have different community standards without imposing them on others
- Cross-posting is an explicit, conscious action
- Spam containment is natural — spam only reaches aggregators that subscribe to the spam feed

### Data Model

#### User Account

```go
type User struct {
    ID          string    // aggregator-local user ID
    FeedURL     string    // user's feed URL (their identity)
    DisplayName string    // cached from feed
    AvatarURL   string    // cached from feed
    Verified    bool      // signature verification status
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
    EntryID       string    // original entry ID from source feed
    FeedURL       string    // feed the entry was ingested from
    InReplyToRef  string    // thr:in-reply-to ref, used for threading
    SourceContent []byte    // original signed Atom entry
    ReceivedAt    time.Time
    SignatureOK   bool
    // Aggregator-local metadata (not part of signed content)
    LocalReplies  int
    LocalLikes    int
    Moderated     bool
}
```

### Registration

A user registers on an aggregator by providing their feed URL. The aggregator:

1. Fetches the feed
2. Verifies at least one entry signature against the published key
3. Issues a registration token (or uses a challenge-response against the feed URL)
4. Creates a local account linked to that feed URL

```
POST /api/register HTTP/1.1
Content-Type: application/json

{
  "feed_url": "https://miki.one/feed.atom"
}
```

The aggregator then challenges the user to prove ownership — e.g., "publish an entry containing this nonce" or "add this token to your `/.well-known/atomic-keys`."

### Following

```
POST /api/follow HTTP/1.1
Authorization: Bearer <aggregator-token>

{
  "feed_url": "https://other.example/feed.atom"
}
```

The aggregator adds the feed to its subscription list. If the feed advertises a WebSub hub, the aggregator subscribes for push updates. Otherwise, it polls at a configurable interval.

### Timeline Generation

The aggregator merges entries from all feeds a user follows into a single timeline. This is served as an Atom feed:

```
GET /timeline/miki.atom
```

The aggregator MAY apply any ordering algorithm — chronological, ranked, filtered — because it's just ordering Atom entries. Different aggregators can compete on timeline quality, just as podcast apps compete on discovery and recommendations.

#### Re-served Entries

When the aggregator serves an entry (in a timeline or on a post page), it:

- sets `link rel="alternate"` to its **own** URL for the post, so replies made from here point here
- adds `link rel="via"` pointing to the URL it got the entry from
- keeps `atom:source` with the original feed's metadata (`id`, `title`, `link rel="self"`)
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
  <!-- original content, thr:in-reply-to, atomic:signature unchanged -->
</entry>
```

### Conversations and Replies

A reply is just an Atom entry on the replier's feed server, with `thr:in-reply-to`. Threads are matched by `ref` (the Atom entry `id`), never by `href`, since every aggregator shows the same post under its own URL.

#### Inside an Island

No WebMention is needed. The aggregator ingests its members' feeds, sees the reply's `thr:in-reply-to ref`, and attaches it to the thread.

#### Forwarding to the Source

When the aggregator ingests a member's reply, it sends a WebMention to the parent post's `via` URL — the author's own post URL if the aggregator got the entry directly from the author's feed. This notifies the author's feed server (or blog) — even if the author is not on this aggregator.

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

An aggregator that wants replies from other islands MAY pull the source's `replies.atom`. Isolation is the default; bridging islands is an explicit choice.

#### Replies from Outside the Island

Alice is on island A. Bob reads A's public timeline but is a member of island B, so A does not ingest Bob's feed. Bob's reply points at A's URL (the `alternate` A served), so:

1. Bob's reply lands in his feed with `thr:in-reply-to href="https://agg-a.example/posts/abc123"`
2. Island B ingests it and sends a WebMention to the endpoint discovered at that `href` — island A's
3. Island A receives `source=https://bob.example/posts/7&target=https://agg-a.example/posts/abc123`
4. Island A fetches the source, verifies it links to the target, verifies the signature
5. Island A adds the reply to the thread and forwards it to Alice's feed server

If Bob had replied to Alice's original URL instead, the reply would go to Alice's feed server only, and island A would not see it. That's consistent with islands.

**Sending rule:** whoever sends a WebMention for a reply sends it to the endpoint discovered at the `in-reply-to` `href`. **Receiving rule:** an aggregator that receives a reply forwards it to the source (via `via`). WebMention is idempotent per source+target, so repeated deliveries are harmless.

#### Islands Consuming Islands

An aggregator's timeline is an Atom feed, so island B can subscribe to island A. B removes duplicates by entry `id` and verifies signatures against the `author` key, independent of which feed the entry arrived through. B sets its own `alternate` and points `via` at A. Replies on B then forward along the `via` chain: B → A → Alice.

#### Spam Control: Proof-of-Work Challenge

WebMention endpoints are open by default, making them spam targets. Both amfeed and amagg protect their endpoints with a proof-of-work challenge using HTTP's standard challenge/response mechanism.

**1. First request, without proof:**

```
POST /webmention HTTP/1.1
Content-Type: application/x-www-form-urlencoded

source=https://bob.example/posts/7&target=https://agg-a.example/posts/abc123
```

```
HTTP/1.1 401 Unauthorized
WWW-Authenticate: PoW challenge="<base64>", difficulty=20
```

**2. Retry with solution:**

```
POST /webmention HTTP/1.1
Authorization: PoW challenge="<base64>", nonce="<hex>"
Content-Type: application/x-www-form-urlencoded

source=https://bob.example/posts/7&target=https://agg-a.example/posts/abc123
```

The solution is valid if `SHA-256(challenge || nonce)` has at least `difficulty` leading zero bits.

- **Stateless challenge:** the challenge encodes an expiry, a random value, and an HMAC (with a server secret) over expiry, random value, source and target. The server stores nothing until a solution arrives, and a challenge only works for the mention it was issued for.
- **Single-use:** the server remembers used challenges until they expire and rejects reuse. One solution buys exactly one WebMention.

The sender is a server (an aggregator forwarding a reply, or a feed server), so the cost lands on infrastructure, not on users. Cheap for one-off mentions, expensive for bulk spam.

### API

#### `GET /timeline/{user}.atom`

User's merged timeline as Atom feed. Supports pagination (`?before=`, `?after=` by entry timestamp).

#### `GET /posts/{id}`

Single post view with thread context. HTML or Atom based on Accept header.

#### `GET /posts/{id}/thread.atom`

Full thread as Atom feed, including cross-aggregator replies received via WebMention.

#### `POST /api/register`

Register a new user account (see Registration above).

#### `POST /api/follow` / `DELETE /api/follow`

Follow or unfollow a feed.

#### `GET /api/following`

List followed feeds for the authenticated user.

#### `POST /webmention`

WebMention endpoint. Accepts incoming mentions with proof-of-work challenge.

#### `GET /api/mentions/{entry-id}`

List WebMentions received for a specific entry.

#### `GET /api/feed?url={feed-url}`

Proxy: fetch (and cache) any Atom feed on behalf of the client — profiles, reply feeds, feeds not yet followed. Token-authenticated, and only returns responses that parse as Atom, so it can't be used as an open proxy.

### Follow Graph Portability

The follow graph is a simple list of feed URLs. An aggregator SHOULD provide an export endpoint:

```
GET /api/following/export
Accept: application/json

{
  "feeds": [
    "https://alice.example/feed.atom",
    "https://bob.example/feed.atom"
  ],
  "exported_at": "2024-12-01T12:00:00Z"
}
```

Switching aggregators = export your follow list, import it on the new one. Your posts stay at your feed URL. Your identity doesn't change.

### Storage

- SQLite (WAL mode) for user accounts, follow graph, merged entries, WebMention records, WebSub subscription state
- Poll scheduling: a `next_poll_at` column and an in-process scheduler goroutine
- Rate limiting: in-memory token buckets
- Timelines are computed at query time (entries joined with follows, ordered), no per-user fan-out

Storage sits behind an interface, so a Postgres backend can be added if an aggregator outgrows SQLite.

---

## 6. amfront — Frontend

The frontend is a pure client. It holds no user data, no social graph, no content. It reads from aggregators and posts to feed servers.

### Responsibilities

1. Authenticate with the user's feed server (posting token)
2. Authenticate with the user's aggregator(s) (session token)
3. Read timeline from aggregator
4. Display entries with threading
5. Compose and publish new entries to feed server

amfront does not send WebMentions. The aggregator ingests the user's reply from their feed and forwards it (see [Conversations and Replies](#conversations-and-replies)).

### Architecture

amfront is a static web application (HTML + JS). It talks to exactly two kinds of servers, both of which send CORS headers:

- **Aggregator(s)** — all reading: timelines, threads, profiles. Feeds from third-party servers are fetched through the aggregator's proxy (`GET /api/feed`), so amfront never needs CORS from arbitrary feed servers.
- **Feed server** — posting only. Posting never goes through the aggregator; otherwise the aggregator would hold the posting token and could post as the user.

The reference implementation is a small Go binary that serves the embedded static files. Any static host works too.

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
2. **Aggregator URL(s) + credentials** — where to read timelines

The reference implementation keeps this in the browser (localStorage). A frontend MAY keep it server-side instead; it is the only state a frontend holds.

### Pages

| Route | Description |
|-------|-------------|
| `/` | Home timeline (merged from connected aggregators) |
| `/post` | Compose new post |
| `/thread/{id}` | Thread view |
| `/profile/{feed-url}` | Profile view (fetched from feed) |
| `/settings` | Configure feed server + aggregator connections |
| `/notifications` | WebMention activity on your posts |

### Posting Flow

1. User writes a post in the compose UI
2. amfront sends `POST /api/entries` to the user's feed server. For a reply, `in_reply_to` carries the parent's `id` as `ref` and the `alternate` URL the user saw as `href`
3. Feed server creates the entry, signs it, adds it to the feed (and pings WebSub)
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

### amfeed

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

# [websub]
# hub_url = "https://websub-hub.example/"
```

### amagg

```toml
# amagg.toml
[server]
listen = ":8200"

[aggregator]
name = "Capy Club"
base_url = "https://capyclub.example"

[polling]
default_interval = "5m"
min_interval = "1m"
max_interval = "1h"

[webmention]
pow_difficulty = 20       # leading zero bits required
challenge_ttl = "10m"
verify_signatures = true

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

1. **Feed discovery** — How does a new user find aggregators to join? A directory? A well-known endpoint? Word of mouth (like early Twitter)?

2. **DMs** — Private messaging is explicitly out of scope for v1. Aggregators could add it as a non-federated feature, or we could spec encrypted entries later.

3. **Rich media** — Media hosting is the feed server's problem. Should amfeed include media upload, or delegate to external hosts?

4. **Feed pagination** — RFC 5005 (Feed Paging and Archiving) provides standard Atom pagination. Should we mandate it?

5. **Aggregator trust** — If you register on an evil aggregator, it has your follow graph and sees your reading patterns. Mitigation: follow graph portability + ability to leave. The aggregator can't forge your posts (signatures), and your content lives on your feed server.

6. **WebSub hub operation** — Run your own hub, use a public hub, or have the aggregator run one? Pragmatically, a few public hubs would emerge.

7. **Entry size limits** — Should feeds enforce a max entry size? Micro-blogging suggests yes (keep it short), but the protocol shouldn't prevent long-form.

8. **Plain WebMention senders** — Blogs (e.g. WordPress) don't solve PoW challenges. Options: accept mentions without proof into a moderation queue, or waive the challenge for sources the aggregator already subscribes to.

9. **Likes in feed readers** — Likes and reposts are entries, so they show up as near-empty items in plain feed readers. Should they live in a separate feed?

10. **Feed servers without an aggregator** — A reply posted to a feed server that no aggregator ingests notifies nobody. Should amfeed also send WebMentions for replies? Duplicates are harmless.

11. **Island-to-island forwarding** — When island B consumes island A, should B forward replies along the `via` chain (B → A → source), or straight to the source, skipping A? To be settled by experiment.

12. **Thread completeness in the UI** — Threads are per-island by design. Frontends should say so honestly ("12 replies here · 30 more elsewhere") and offer the source's `replies.atom` for the full picture.

---

## 12. Implementation Roadmap

### Phase 1: Foundation

- [ ] `amfeed`: Atom feed generation, entry CRUD, token auth, SQLite storage
- [ ] `amfeed`: Entry signing (Ed25519, field-based)
- [ ] `amagg`: Feed polling, user registration, follow graph, SQLite storage
- [ ] `amagg`: Timeline generation (chronological), re-served entries (`alternate`, `via`, `atom:source`)
- [ ] `amagg`: Feed proxy
- [ ] `amfront`: Basic static web UI — read timeline, compose posts

### Phase 2: Interactions

- [ ] `amagg`: In-island threading from member feeds
- [ ] `amfeed` + `amagg`: WebMention endpoint (incoming), reply feeds
- [ ] `amagg`: WebMention forwarding to source
- [ ] `amfeed` + `amagg`: Proof-of-work challenge (issue, verify, single-use)
- [ ] `amfront`: Reply threading UI (using RFC 4685)
- [ ] `amfront`: Like/repost actions

### Phase 3: Real-Time

- [ ] `amfeed`: WebSub hub announcement
- [ ] `amagg`: WebSub subscription management
- [ ] `amfront`: Live timeline updates (SSE or WebSocket from amagg)

### Phase 4: Polish

- [ ] `amagg`: Signature verification on ingest
- [ ] `amagg`: Moderation tools
- [ ] `amagg`: Follow graph export/import
- [ ] `amfront`: Multi-aggregator support
- [ ] `amfront`: Media upload flow
- [ ] Documentation site at atomicmicro.blog
