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
)

type Server struct {
	cfg     *Config
	store   *Store
	client  *http.Client
	keys    *sign.KeyCache
	origin  string // scheme://host of the base URL, bound into login challenges
	now     func() time.Time
	handler http.Handler

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
		cfg:    cfg,
		store:  store,
		client: client,
		keys:   sign.NewKeyCache(client),
		origin: u.Scheme + "://" + u.Host,
		now:    time.Now,
		logins: map[string]pendingLogin{},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handleHome)
	mux.HandleFunc("GET /timeline.atom", s.handleIslandTimeline)
	mux.HandleFunc("GET /timeline/{file}", s.handleUserTimeline)
	mux.HandleFunc("GET /posts/{n}", s.handlePost)
	mux.HandleFunc("POST /api/login", s.handleLogin)
	mux.HandleFunc("POST /api/login/verify", s.handleLoginVerify)
	mux.HandleFunc("POST /api/follow", s.requireUser(s.handleFollow))
	mux.HandleFunc("DELETE /api/follow", s.requireUser(s.handleUnfollow))
	mux.HandleFunc("GET /api/following", s.requireUser(s.handleFollowing))
	mux.HandleFunc("GET /api/following/export", s.requireUser(s.handleFollowingExport))
	s.handler = web.CORS(mux, "GET, POST, DELETE, OPTIONS")
	return s, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.handler.ServeHTTP(w, r) }

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

var errNoCredentials = errors.New("no credentials")

// authUser returns the member a request authenticates as.
func (s *Server) authUser(r *http.Request) (*User, error) {
	token, ok := web.BearerToken(r)
	if !ok {
		return nil, errNoCredentials
	}
	return s.store.UserByToken(web.HashToken(token))
}

// checkUser writes a 401 (or 500) and returns nil if the request doesn't
// authenticate as a member.
func (s *Server) checkUser(w http.ResponseWriter, r *http.Request) *User {
	u, err := s.authUser(r)
	switch {
	case err == nil:
		return u
	case errors.Is(err, errNoCredentials):
		w.Header().Set("WWW-Authenticate", "Bearer")
		web.WriteError(w, http.StatusUnauthorized, "member credentials required")
	case errors.Is(err, ErrNotFound):
		w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
		web.WriteError(w, http.StatusUnauthorized, "invalid token")
	default:
		web.ServerError(w, err)
	}
	return nil
}

func (s *Server) requireUser(h func(http.ResponseWriter, *http.Request, *User)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if u := s.checkUser(w, r); u != nil {
			h(w, r, u)
		}
	}
}

// checkPublic lets the request through on a public island, and otherwise
// requires member credentials. It reports whether to continue.
func (s *Server) checkPublic(w http.ResponseWriter, r *http.Request) bool {
	return s.cfg.Aggregator.Public || s.checkUser(w, r) != nil
}
