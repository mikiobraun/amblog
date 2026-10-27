// Package amagg implements the aggregator: members log in with their
// identity URL, follow feeds, and read merged timelines. See the spec,
// "amagg — Aggregator".
package amagg

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"atomicmicro.blog/internal/island"
	"atomicmicro.blog/internal/web"
)

// Server is amagg's own part: members, login, follows, timelines and pages.
// Following feeds, ingest, WebSub and WebMentions are the island's.
type Server struct {
	cfg     *Config
	store   *Store
	island  *island.Island
	client  *http.Client
	origin  string // scheme://host of the base URL, bound into login challenges
	basicOK bool   // HTTP Basic allowed: base URL is https, or on a loopback host
	now     func() time.Time
	handler http.Handler
	proxy   proxyCache

	mu     sync.Mutex
	logins map[string]pendingLogin // by challenge token
}

func NewServer(cfg *Config, store *Store) (*Server, error) {
	u, err := url.Parse(cfg.Aggregator.BaseURL)
	if err != nil {
		return nil, err
	}
	s := &Server{
		cfg:     cfg,
		store:   store,
		origin:  u.Scheme + "://" + u.Host,
		basicOK: u.Scheme == "https" || island.IsLoopback(u.Hostname()),
		now:     time.Now,
		logins:  map[string]pendingLogin{},
	}
	p := cfg.Polling
	s.island = island.New(island.Options{
		BaseURL:         cfg.Aggregator.BaseURL,
		DefaultInterval: p.DefaultInterval.Duration,
		MinInterval:     p.MinInterval.Duration,
		MaxInterval:     p.MaxInterval.Duration,
		PerHostPerHour:  cfg.WebMention.PerHostPerHour,
		BlockHosts:      cfg.WebMention.BlockHosts,
		Target:          s.target,
	}, store.Store)
	s.island.Now = func() time.Time { return s.now() } // one clock, which tests set
	s.client = s.island.Client

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handleHome)
	mux.HandleFunc("GET /timeline.atom", s.handleIslandTimeline)
	mux.HandleFunc("GET /timeline/{file}", s.handleUserTimeline)
	mux.HandleFunc("GET /posts/{n}", s.handlePost)
	mux.HandleFunc("GET /posts/{n}/thread.atom", s.handleThread)
	mux.HandleFunc("POST /webmention", s.island.HandleWebMention)
	mux.HandleFunc("GET /websub/{feed}", s.island.HandleWebSubVerify)
	mux.HandleFunc("POST /websub/{feed}", s.island.HandleWebSubPush)
	mux.HandleFunc("POST /api/login", s.handleLogin)
	mux.HandleFunc("POST /api/login/verify", s.handleLoginVerify)
	mux.HandleFunc("POST /api/follow", s.requireUser(s.handleFollow))
	mux.HandleFunc("DELETE /api/follow", s.requireUser(s.handleUnfollow))
	mux.HandleFunc("GET /api/following", s.requireUser(s.handleFollowing))
	mux.HandleFunc("GET /api/following/export", s.requireUser(s.handleFollowingExport))
	mux.HandleFunc("GET /api/feed", s.requireUser(s.handleProxy))
	mux.HandleFunc("POST /api/readers", s.requireUser(s.handleCreateReader))
	mux.HandleFunc("GET /api/readers", s.requireUser(s.handleListReaders))
	mux.HandleFunc("DELETE /api/readers/{id}", s.requireUser(s.handleDeleteReader))
	s.handler = web.CORS(mux, "GET, POST, DELETE, OPTIONS")
	return s, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.handler.ServeHTTP(w, r) }

// RunPoller polls followed feeds until ctx is cancelled.
func (s *Server) RunPoller(ctx context.Context) { s.island.RunPoller(ctx) }

// PollDue polls the feeds whose next poll is due.
func (s *Server) PollDue() { s.island.PollDue() }

// wait blocks until background work (received WebMentions, forwarded
// replies) is done.
func (s *Server) wait() { s.island.Wait() }

func (s *Server) postURL(n int64) string {
	return s.cfg.Aggregator.BaseURL + "/posts/" + strconv.FormatInt(n, 10)
}

// target returns the entry at one of our post URLs, for incoming WebMentions.
func (s *Server) target(u string) (*island.Entry, bool, error) {
	rest, ok := strings.CutPrefix(u, s.cfg.Aggregator.BaseURL+"/posts/")
	if !ok {
		return nil, false, nil
	}
	n, err := strconv.ParseInt(rest, 10, 64)
	if err != nil || n <= 0 {
		return nil, false, nil
	}
	e, err := s.store.EntryByN(n)
	if errors.Is(err, island.ErrNotFound) {
		return nil, false, nil
	}
	return e, err == nil, err
}

func (s *Server) timelineURL(u *User) string {
	return s.cfg.Aggregator.BaseURL + "/timeline/" + u.Name + ".atom"
}

// userName derives a member's name (used in URLs and as feed reader user
// name) from the author URI: host and path, with "/" and ":" replaced by "_".
func userName(authorURI string) string {
	u, err := url.Parse(authorURI)
	if err != nil {
		return ""
	}
	return strings.NewReplacer("/", "_", ":", "_").Replace(u.Host + strings.TrimSuffix(u.Path, "/"))
}

var (
	errNoCredentials = errors.New("member credentials required")
	errBasicNotFeed  = errors.New("HTTP Basic credentials are only accepted on feeds")
	errBasicInsecure = errors.New("HTTP Basic credentials need an https aggregator")
)

// authUser returns the member a request authenticates as: by bearer token,
// or, on feeds (feed = true), by a feed reader's HTTP Basic password.
func (s *Server) authUser(r *http.Request, feed bool) (*User, error) {
	if token, ok := web.BearerToken(r); ok {
		return s.store.UserByToken(web.HashToken(token))
	}
	if name, password, ok := r.BasicAuth(); ok {
		if !feed {
			return nil, errBasicNotFeed
		}
		if !s.basicOK {
			return nil, errBasicInsecure
		}
		return s.store.UserByReader(name, web.HashToken(password))
	}
	return nil, errNoCredentials
}

// checkUser writes a 401 (or 403, 500) and returns nil if the request doesn't
// authenticate as a member. feed: the request is for a feed, where feed
// readers may use HTTP Basic.
func (s *Server) checkUser(w http.ResponseWriter, r *http.Request, feed bool) *User {
	u, err := s.authUser(r, feed)
	if err == nil {
		return u
	}
	challenge := func() {
		w.Header().Add("WWW-Authenticate", "Bearer")
		if feed && s.basicOK {
			w.Header().Add("WWW-Authenticate", `Basic realm="`+s.cfg.Aggregator.Name+`"`)
		}
	}
	switch {
	case errors.Is(err, errNoCredentials), errors.Is(err, errBasicNotFeed):
		challenge()
		web.WriteError(w, http.StatusUnauthorized, err.Error())
	case errors.Is(err, ErrNotFound):
		challenge()
		web.WriteError(w, http.StatusUnauthorized, "invalid credentials")
	case errors.Is(err, errBasicInsecure):
		web.WriteError(w, http.StatusForbidden, err.Error())
	default:
		web.ServerError(w, err)
	}
	return nil
}

func (s *Server) requireUser(h func(http.ResponseWriter, *http.Request, *User)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if u := s.checkUser(w, r, false); u != nil {
			h(w, r, u)
		}
	}
}

// checkPublic lets the request through on a public island, and otherwise
// requires member credentials. It reports whether to continue.
func (s *Server) checkPublic(w http.ResponseWriter, r *http.Request, feed bool) bool {
	return s.cfg.Aggregator.Public || s.checkUser(w, r, feed) != nil
}
