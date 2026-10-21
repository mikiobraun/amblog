# ⚛ Atomic Micro Blogging

**Social networking, split into its atoms.**

Every social network so far bundles three different jobs into one server: keeping your posts, deciding what you see, and showing it to you. Whoever runs that server owns all three, and with them your audience, your identity and your timeline.

Atomic Micro Blogging takes them apart:

| Atom | What it does | Who runs it |
|------|-------------|-------------|
| **amfeed** — publish | Your posts, as a signed Atom feed, at your own URL. Your URL *is* your identity. | You (or anyone you trust — it's tiny) |
| **amagg** — aggregate | Follows feeds, merges them into timelines, hosts the conversations. A room with its own vibe and rules. | Communities, friends, companies — many of them, competing |
| **amfront** — present | Reads timelines, writes posts. Web, app, CLI, Emacs mode — whatever you like. | Anyone |

Leave an aggregator and you take your followers list along; your posts never moved anyway. Don't like the app? Use another one on the same timeline. It works like podcasts: one open feed format, lots of apps and directories competing on top of it.

## Built from what already exists

No new protocol. Posts are [Atom](https://www.rfc-editor.org/rfc/rfc4287) entries, threads are [Atom threading](https://www.rfc-editor.org/rfc/rfc4685), updates go out via [WebSub](https://www.w3.org/TR/websub/), and replies cross between communities via [WebMention](https://www.w3.org/TR/webmention/). We add only a little: Ed25519 signatures on entries so nobody can forge your posts, and a proof-of-work stamp to keep spam out.

That means any feed reader can follow you today, and any blog that speaks WebMention can join the conversation.

## Status

Early days. What works:

- **amfeed** — publish, reply, delete, signed entries, paged feeds
- **am** — a command-line client
- **amfront** — a web frontend that checks every signature in your browser

Next up: **amagg**, where the social part happens.

The whole design lives in [atomic-micro-blogging.md](atomic-micro-blogging.md).

## Try it

```sh
go install atomicmicro.blog/cmd/amfeed@latest atomicmicro.blog/cmd/am@latest

amfeed init -dir myfeed -base-url http://localhost:8100 -name "Your Name"
amfeed serve -config myfeed/amfeed.toml

am config -feed http://localhost:8100 -token <the admin token from init>
am post "Hello, atomic world"
am show http://localhost:8100/feed.atom
```

For the web frontend: `cd amfront && npm install && npm run dev`, then open http://localhost:8300.

---

*atomicmicro.blog — the dream of a better web, retro-futurism included.*
