// Package island is what every island shares: it follows feeds (polling and
// WebSub), ingests their entries with verification, receives WebMentions for
// its posts and forwards replies to their sources. amagg (many members) and
// ampress (one feed) build their pages on it. See the spec, "Ingest and
// Verification" and "Conversations and Replies".
package island

import (
	"net/http"
	"regexp"
	"strconv"
	"sync"
	"time"

	"atomicmicro.blog/internal/atom"
	"atomicmicro.blog/internal/sign"
	"atomicmicro.blog/internal/webmention"
)

type Options struct {
	BaseURL         string // the island's URL, without trailing slash
	DefaultInterval time.Duration
	MinInterval     time.Duration
	MaxInterval     time.Duration
	PerHostPerHour  int      // incoming WebMentions per sending host
	BlockHosts      []string // hosts whose WebMentions are rejected
	// Target returns the entry one of the island's URLs shows, for incoming
	// WebMentions. ok is false for URLs that aren't posts of the island.
	Target func(url string) (e *Entry, ok bool, err error)
	// Ingested, if set, is called after an entry was stored or replaced, with
	// the entry as stored (author filled in). An error fails the ingest.
	Ingested func(e *Entry, ae *atom.Entry) error
}

type Island struct {
	opts    Options
	Store   *Store
	Client  *http.Client
	Now     func() time.Time
	keys    *sign.KeyCache
	limiter *webmention.Limiter
	bg      sync.WaitGroup // background work, see Wait

	// feedMu serializes updates of a feed's polling state, which polls and
	// WebSub pushes both make.
	feedMu sync.Mutex
}

func New(opts Options, store *Store) *Island {
	client := &http.Client{Timeout: 20 * time.Second}
	return &Island{
		opts:    opts,
		Store:   store,
		Client:  client,
		Now:     time.Now,
		keys:    sign.NewKeyCache(client),
		limiter: webmention.NewLimiter(opts.PerHostPerHour),
	}
}

// Wait blocks until background work (received WebMentions, forwarded
// replies) is done.
func (is *Island) Wait() { is.bg.Wait() }

func (is *Island) background(f func()) {
	is.bg.Add(1)
	go func() {
		defer is.bg.Done()
		f()
	}()
}

func (is *Island) WebMentionURL() string { return is.opts.BaseURL + "/webmention" }

func (is *Island) callbackURL(feedID int64) string {
	return is.opts.BaseURL + "/websub/" + strconv.FormatInt(feedID, 10)
}

// Entries come from arbitrary feeds, so their HTML is untrusted. Pages are
// served with a CSP that forbids scripts, forms and plugins (sandbox), which
// makes rendering that HTML as is safe enough for a plain page.
const PageCSP = "default-src 'none'; img-src * data:; style-src 'unsafe-inline'; sandbox"

var anchorRe = regexp.MustCompile(`(?i)<a(\s|>)`)

// Nofollow marks every link in a reply as user content, so links in replies
// carry no search ranking (see the spec, "Spam Control"). Our rel comes first,
// and browsers use the first of duplicate attributes.
func Nofollow(html string) string {
	return anchorRe.ReplaceAllString(html, `<a rel="nofollow ugc"$1`)
}
