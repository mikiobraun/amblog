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

And because every piece speaks plain Atom, they compose: **ampress** turns any one feed into a blog, with replies arriving from anywhere via WebMention. Point amfeed at a folder of Markdown files, point ampress at that feed, and a folder becomes a blog people can reply to.

## Built from what already exists

No new protocol. Posts are [Atom](https://www.rfc-editor.org/rfc/rfc4287) entries, threads are [Atom threading](https://www.rfc-editor.org/rfc/rfc4685), updates go out via [WebSub](https://www.w3.org/TR/websub/), and replies cross between communities via [WebMention](https://www.w3.org/TR/webmention/). We add only a little: Ed25519 signatures on entries, so nobody can forge your posts. Spam is handled by taking away the payoff instead of building gates: links in replies carry no ranking, replies reach only their thread, and senders are rate-limited and can be blocked.

That means any feed reader can follow you today, and any blog that speaks WebMention can join the conversation.

## Status

Early, but the whole loop works:

- **amfeed** — publish, reply, delete, signed entries, paged feeds, its own WebSub hub, WebMentions, and a content folder that turns Markdown files into posts
- **amagg** — login with your identity URL, follows, island and personal timelines, threads, replies forwarded across islands, feed reader access
- **ampress** — a blog for one feed: index, post pages with replies, about page, themes
- **amfront** — a web frontend that checks every signature in your browser
- **am** — a command-line client

Next: moderation, likes and reposts, islands following islands, comments on ampress.

The whole design lives in [atomic-micro-blogging.md](atomic-micro-blogging.md).

## Try it

```sh
go install atomicmicro.blog/cmd/...@latest   # amfeed, amagg, ampress, am

# Your feed
amfeed init -dir myfeed -base-url http://localhost:8100 -name "Your Name"
amfeed serve -config myfeed/amfeed.toml

am config -feed http://localhost:8100 -token <the admin token from init>
am post "Hello, atomic world"
am show http://localhost:8100/feed.atom

# An aggregator, and you in it
amagg init -dir myagg -base-url http://localhost:8200 -name "My Island"
amagg serve -config myagg/amagg.toml
am login -invite "$(amagg invite -config myagg/amagg.toml)" http://localhost:8200
am timeline

# Your feed as a blog
ampress init -dir myblog -base-url http://localhost:8400 -title "My Blog" -feed http://localhost:8100/feed.atom
ampress serve -config myblog/ampress.toml
```

For the web frontend: `cd amfront && npm install && npm run dev`, then open http://localhost:8300.

From a checkout, `make dev` sets up a whole local network instead: three feeds (alice, bob, carol) and an island, with posts, logins and follows.

## License

BSD 3-Clause, see [LICENSE](LICENSE).

---

*atomicmicro.blog — the dream of a better web, retro-futurism included.*
