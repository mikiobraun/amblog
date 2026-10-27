// Package ampress turns one feed into a blog: an island with one feed,
// served as web pages with the replies its posts receive. See the spec,
// "ampress — Blog".
package ampress

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"atomicmicro.blog/internal/atom"
	"atomicmicro.blog/internal/island"
)

// The blog's feed usually pushes via WebSub; polling is the fallback.
const (
	defaultInterval = 5 * time.Minute
	minInterval     = time.Minute
	maxInterval     = time.Hour
)

type Server struct {
	cfg     *Config
	store   *Store
	island  *island.Island
	theme   *theme
	feedID  int64 // the blog's feed
	now     func() time.Time
	handler http.Handler
}

func NewServer(cfg *Config, store *Store) (*Server, error) {
	th, err := loadTheme(cfg.Blog.Theme)
	if err != nil {
		return nil, err
	}
	s := &Server{cfg: cfg, store: store, theme: th, now: time.Now}
	s.island = island.New(island.Options{
		BaseURL:         cfg.Blog.BaseURL,
		DefaultInterval: defaultInterval,
		MinInterval:     minInterval,
		MaxInterval:     maxInterval,
		PerHostPerHour:  cfg.WebMention.PerHostPerHour,
		BlockHosts:      cfg.WebMention.BlockHosts,
		Target:          s.target,
		Ingested:        s.ingested,
	}, store.Store)
	s.island.Now = func() time.Time { return s.now() } // one clock, which tests set

	f, err := s.island.AddFeed(cfg.Blog.Feed)
	if err != nil {
		return nil, err
	}
	if err := store.SetBlogFeed(f.ID); err != nil {
		return nil, err
	}
	s.feedID = f.ID
	if err := s.rebuildPosts(); err != nil {
		return nil, err
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handleIndex)
	mux.HandleFunc("GET /posts/{slug}", s.handlePost)
	mux.HandleFunc("GET /about", s.handleAbout)
	mux.HandleFunc("GET /feed.atom", s.handleFeed)
	mux.HandleFunc("GET /static/{file...}", s.handleStatic)
	mux.HandleFunc("POST /webmention", s.island.HandleWebMention)
	mux.HandleFunc("GET /websub/{feed}", s.island.HandleWebSubVerify)
	mux.HandleFunc("POST /websub/{feed}", s.island.HandleWebSubPush)
	s.handler = mux
	return s, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.handler.ServeHTTP(w, r) }

// RunPoller follows the blog's feed until ctx is cancelled.
func (s *Server) RunPoller(ctx context.Context) { s.island.RunPoller(ctx) }

// PollDue polls the feed if its next poll is due.
func (s *Server) PollDue() { s.island.PollDue() }

// Wait blocks until background work (received WebMentions, forwarded
// replies) is done.
func (s *Server) Wait() { s.island.Wait() }

func (s *Server) postURL(slug string) string {
	return s.cfg.Blog.BaseURL + "/posts/" + url.PathEscape(slug)
}

// slugOf returns the last segment of an entry ID: everything after the last
// ":" or "/".
func slugOf(id string) string {
	return id[strings.LastIndexAny(id, ":/")+1:]
}

func isTombstone(e *atom.Entry) bool {
	return (e.Content == nil || e.Content.Body == "") && e.Title == "" && e.Verb == ""
}

// ingested keeps the posts table current: top-level entries of the blog's
// feed are posts under their slug; replies, likes and reposts aren't.
func (s *Server) ingested(e *island.Entry, ae *atom.Entry) error {
	if !e.FeedID.Valid || e.FeedID.Int64 != s.feedID {
		return nil
	}
	if ae.InReplyTo != nil || ae.Verb != "" {
		return s.store.DeletePost(e.N)
	}
	slug := slugOf(ae.ID)
	if slug == "" {
		log.Printf("ampress: entry %q has no slug, not shown", ae.ID)
		return nil
	}
	err := s.store.SavePost(e.N, slug, isTombstone(ae))
	if errors.Is(err, errSlugTaken) {
		log.Printf("ampress: slug %q of entry %q belongs to another post, not shown", slug, ae.ID)
		return nil
	}
	return err
}

// rebuildPosts derives the posts table from the blog feed's stored entries,
// in the order they were first stored, so the first to claim a slug keeps
// it. Runs at startup: after a switch of feeds, the new feed's entries may
// all be stored already, and a poll brings nothing new.
func (s *Server) rebuildPosts() error {
	if err := s.store.ClearPosts(); err != nil {
		return err
	}
	entries, err := s.store.FeedEntries(s.feedID)
	if err != nil {
		return err
	}
	for _, e := range entries {
		doc, err := atom.Parse([]byte(e.XML))
		if err != nil || len(doc.Entries) != 1 {
			return fmt.Errorf("stored entry %d: %v", e.N, err)
		}
		if err := s.ingested(e, doc.Entries[0]); err != nil {
			return err
		}
	}
	return nil
}

// target returns the post at one of our post URLs, for incoming WebMentions.
func (s *Server) target(u string) (*island.Entry, bool, error) {
	rest, ok := strings.CutPrefix(u, s.cfg.Blog.BaseURL+"/posts/")
	if !ok {
		return nil, false, nil
	}
	slug, err := url.PathUnescape(rest)
	if err != nil {
		return nil, false, nil
	}
	p, err := s.store.PostBySlug(slug)
	if errors.Is(err, island.ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return p.Entry, !p.Deleted, nil
}

// reserve prepares a stored entry for serving from the blog: the post page
// as alternate (for posts), via pointing where the entry came from, and
// atom:source with the blog's feed. None of these are signed.
func (s *Server) reserve(e *island.Entry, slug string) (*atom.Entry, error) {
	doc, err := atom.Parse([]byte(e.XML))
	if err != nil || len(doc.Entries) != 1 {
		return nil, fmt.Errorf("stored entry %d: %v", e.N, err)
	}
	ae := doc.Entries[0]
	via := island.LinkHref(ae.Links, "alternate")
	if via == "" {
		via = e.FetchedFrom
	}
	var links []atom.Link
	if slug != "" {
		links = append(links, atom.Link{Rel: "alternate", Type: "text/html", Href: s.postURL(slug)})
	}
	for _, l := range ae.Links {
		if l.Rel != "alternate" && l.Rel != "" && l.Rel != "via" {
			links = append(links, l)
		}
	}
	ae.Links = append(links, atom.Link{Rel: "via", Href: via})
	if ae.Source == nil && e.FeedID.Valid && e.FeedID.Int64 == s.feedID {
		f, err := s.store.GetFeed(s.feedID)
		if err != nil {
			return nil, err
		}
		id := f.AtomID
		if id == "" {
			id = f.URL
		}
		ae.Source = &atom.Source{ID: id, Title: f.Title, Links: []atom.Link{{Rel: "self", Href: f.URL, Type: atom.MediaType}}}
	}
	return ae, nil
}
