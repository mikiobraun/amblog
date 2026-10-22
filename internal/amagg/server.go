// Package amagg implements the aggregator: members log in with their
// identity URL, follow feeds, and read merged timelines. See the spec,
// "amagg — Aggregator".
package amagg

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"atomicmicro.blog/internal/sign"
	"atomicmicro.blog/internal/web"
	"atomicmicro.blog/internal/webmention"
)

type Server struct {
	cfg     *Config
	store   *Store
	client  *http.Client
	keys    *sign.KeyCache
	origin  string // scheme://host of the base URL, bound into login challenges
	basicOK bool   // HTTP Basic allowed: base URL is https, or on a loopback host
	now     func() time.Time
	handler http.Handler
	proxy   proxyCache
	limiter *webmention.Limiter
	bg      sync.WaitGroup // background work, see wait

	// feedMu serializes updates of a feed's polling state, which polls and
	// WebSub pushes both make.
	feedMu sync.Mutex

	mu     sync.Mutex
	logins map[string]pendingLogin // by challenge token
}

func NewServer(cfg *Config, store *Store) (*Server, error) {
	u, err := url.Parse(cfg.Aggregator.BaseURL)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 20 * time.Second}
	s := &Server{
		cfg:     cfg,
		store:   store,
		client:  client,
		keys:    sign.NewKeyCache(client),
		origin:  u.Scheme + "://" + u.Host,
		basicOK: u.Scheme == "https" || isLoopback(u.Hostname()),
		now:     time.Now,
		logins:  map[string]pendingLogin{},
		limiter: webmention.NewLimiter(cfg.WebMention.PerHostPerHour),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handleHome)
	mux.HandleFunc("GET /timeline.atom", s.handleIslandTimeline)
	mux.HandleFunc("GET /timeline/{file}", s.handleUserTimeline)
	mux.HandleFunc("GET /posts/{n}", s.handlePost)
	mux.HandleFunc("GET /posts/{n}/thread.atom", s.handleThread)
	mux.HandleFunc("POST /webmention", s.handleWebMention)
	mux.HandleFunc("GET /websub/{feed}", s.handleWebSubVerify)
	mux.HandleFunc("POST /websub/{feed}", s.handleWebSubPush)
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

// wait blocks until background work (received WebMentions, forwarded
// replies) is done.
func (s *Server) wait() { s.bg.Wait() }

func (s *Server) background(f func()) {
	s.bg.Add(1)
	go func() {
		defer s.bg.Done()
		f()
	}()
}

func (s *Server) postURL(n int64) string {
	return s.cfg.Aggregator.BaseURL + "/posts/" + strconv.FormatInt(n, 10)
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
