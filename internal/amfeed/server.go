// Package amfeed implements the feed server: it stores your posts, signs
// them, and serves them as Atom.
package amfeed

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"atomicmicro.blog/internal/atom"
	"atomicmicro.blog/internal/paging"
	"atomicmicro.blog/internal/sign"
	"atomicmicro.blog/internal/web"
	"atomicmicro.blog/internal/webmention"
	"atomicmicro.blog/internal/websub"
)

const (
	feedPageSize    = 50
	defaultPageSize = 20
	maxPageSize     = 100
	// Atom requires feed/updated; an empty feed has never been updated.
	emptyFeedUpdated = "1970-01-01T00:00:00Z"

	challengeTTL      = 10 * time.Minute
	maxChallengeValue = 256
)

type Server struct {
	cfg          *Config
	store        *Store
	key          ed25519.PrivateKey
	idPrefix     string // "tag:<host>,", followed by "<YYYY-MM>:<idPath>post-<n>"
	idPath       string // base URL port and path plus "/", or "" when it has neither
	keysURL      string
	feedPageSize int
	now          func() time.Time
	handler      http.Handler
	client       *http.Client // for WebSub pushes and WebMentions
	limiter      *webmention.Limiter
	bg           sync.WaitGroup // background deliveries, see Wait

	mu         sync.Mutex
	challenges map[string]challenge // login challenges by token, kept in memory
}

// challenge is a published login challenge (see the spec, "Registration and Login").
type challenge struct {
	value   string
	expires time.Time
}

func NewServer(cfg *Config, store *Store, key ed25519.PrivateKey) (*Server, error) {
	u, err := url.Parse(cfg.Feed.BaseURL)
	if err != nil {
		return nil, err
	}
	keysURL, err := sign.KeysURL(cfg.Feed.AuthorURI)
	if err != nil {
		return nil, err
	}
	// Tag URI authorities have no port, so a port goes into the path part:
	// feeds on the same host must not mint the same ids.
	idPath := strings.Trim(u.Port()+"/"+strings.Trim(u.Path, "/"), "/")
	if idPath != "" {
		idPath += "/"
	}
	s := &Server{cfg: cfg, store: store, key: key, idPrefix: "tag:" + u.Hostname() + ",", idPath: idPath, keysURL: keysURL,
		feedPageSize: feedPageSize, now: time.Now, challenges: map[string]challenge{},
		client:  &http.Client{Timeout: 20 * time.Second},
		limiter: webmention.NewLimiter(cfg.WebMention.PerHostPerHour)}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handleProfile)
	mux.HandleFunc("GET /feed.atom", s.handleFeed)
	mux.HandleFunc("GET /posts/{seq}", s.handlePost)
	mux.HandleFunc("GET /posts/{seq}/replies.atom", s.handleReplies)
	mux.HandleFunc("POST "+websub.HubPath, s.handleHub)
	mux.HandleFunc("POST /webmention", s.handleWebMention)
	mux.HandleFunc("GET /"+sign.KeysPath, s.handleKeys)
	mux.HandleFunc("GET /"+sign.ChallengePath+"/{token}", s.handleGetChallenge)
	mux.HandleFunc("POST /api/entries", s.requireToken(false, s.handleCreateEntry))
	mux.HandleFunc("GET /api/entries", s.requireToken(false, s.handleListEntries))
	mux.HandleFunc("DELETE /api/entries/{seq}", s.requireToken(false, s.handleDeleteEntry))
	mux.HandleFunc("POST /api/tokens", s.requireToken(true, s.handleCreateToken))
	mux.HandleFunc("PUT /api/challenges/{token}", s.requireToken(false, s.handlePutChallenge))
	s.handler = web.CORS(mux, "GET, POST, PUT, DELETE, OPTIONS")
	return s, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.handler.ServeHTTP(w, r) }

// Wait blocks until background work (WebSub pushes, WebMentions sent and
// received) is done.
func (s *Server) Wait() { s.bg.Wait() }

func (s *Server) background(f func()) {
	s.bg.Add(1)
	go func() {
		defer s.bg.Done()
		f()
	}()
}

// --- Atom ---

func (s *Server) feedURL() string { return s.cfg.Feed.BaseURL + "/feed.atom" }

func (s *Server) feedPageURL(before *paging.Cursor) string {
	if before == nil {
		return s.feedURL()
	}
	return s.feedURL() + "?before=" + url.QueryEscape(before.String())
}

func (s *Server) postURL(seq int64) string {
	return s.cfg.Feed.BaseURL + "/posts/" + strconv.FormatInt(seq, 10)
}

func (s *Server) author() *atom.Person {
	return &atom.Person{Name: s.cfg.Feed.AuthorName, URI: s.cfg.Feed.AuthorURI}
}

func (s *Server) atomEntry(e *Entry) *atom.Entry {
	ae := &atom.Entry{
		ID:        e.ID,
		Title:     e.Title,
		Published: e.Published,
		Updated:   e.Updated,
		Author:    s.author(),
		Content:   &atom.Content{Type: e.ContentType, Body: e.Content},
		Links:     []atom.Link{{Rel: "alternate", Type: "text/html", Href: s.postURL(e.Seq)}},
		Verb:      e.Verb,
	}
	if e.InReplyToRef != "" {
		ae.InReplyTo = &atom.InReplyTo{Ref: e.InReplyToRef, Href: e.InReplyToHref}
	}
	if e.Signature != "" {
		ae.Signature = &atom.Signature{KeyID: e.KeyID, Algorithm: sign.Algorithm, Value: e.Signature}
	}
	return ae
}

// sign (re)computes the entry's signature from its current fields.
func (s *Server) sign(e *Entry) error {
	ae := s.atomEntry(e)
	if err := sign.Sign(ae, s.key, s.cfg.Signing.KeyID); err != nil {
		return err
	}
	e.KeyID = ae.Signature.KeyID
	e.Signature = ae.Signature.Value
	return nil
}

// handleFeed serves the feed in pages (RFC 5005 paged feed), most recently
// updated first, so edits and deletes reach subscribers on their next poll.
func (s *Server) handleFeed(w http.ResponseWriter, r *http.Request) {
	before, ok := cursorParam(w, r)
	if !ok {
		return
	}
	body, err := s.feedPage(before)
	if err != nil {
		s.serverError(w, err)
		return
	}
	writeAtom(w, r, body)
}

func (s *Server) hubURL() string { return s.cfg.Feed.BaseURL + websub.HubPath }

// feedPage renders one page of the feed; before = nil is the first page.
func (s *Server) feedPage(before *paging.Cursor) ([]byte, error) {
	entries, next, err := s.store.ListEntries(before, s.feedPageSize)
	if err != nil {
		return nil, err
	}
	updated, err := s.store.LatestUpdated()
	if err != nil {
		return nil, err
	}
	if updated == "" {
		updated = emptyFeedUpdated
	}
	f := &atom.Feed{
		ID:      s.feedURL(),
		Title:   s.cfg.Feed.Title,
		Updated: updated,
		Author:  s.author(),
		Links: []atom.Link{
			{Rel: "self", Href: s.feedPageURL(before), Type: atom.MediaType},
			{Rel: "first", Href: s.feedURL(), Type: atom.MediaType},
			{Rel: "alternate", Href: s.cfg.Feed.BaseURL + "/", Type: "text/html"},
			{Rel: "hub", Href: s.hubURL()},
		},
	}
	if next != nil {
		f.Links = append(f.Links, atom.Link{Rel: "next", Href: s.feedPageURL(next), Type: atom.MediaType})
	}
	for _, e := range entries {
		f.Entries = append(f.Entries, s.atomEntry(e))
	}
	return atom.MarshalFeed(f)
}

// writeAtom writes an Atom feed with an ETag, answering If-None-Match with 304.
func writeAtom(w http.ResponseWriter, r *http.Request, body []byte) {
	sum := sha256.Sum256(body)
	etag := `"` + hex.EncodeToString(sum[:16]) + `"`
	w.Header().Set("ETag", etag)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", atom.MediaType+"; charset=utf-8")
	w.Write(body)
}

func (s *Server) handlePost(w http.ResponseWriter, r *http.Request) {
	e, ok := s.entryFromPath(w, r)
	if !ok {
		return
	}
	w.Header().Set("Link", "<"+s.webmentionURL()+`>; rel="webmention"`)
	if strings.Contains(r.Header.Get("Accept"), atom.MediaType) {
		body, err := atom.MarshalEntry(s.atomEntry(e))
		if err != nil {
			s.serverError(w, err)
			return
		}
		w.Header().Set("Content-Type", atom.EntryMediaType+"; charset=utf-8")
		w.Write(body)
		return
	}
	status := http.StatusOK
	if e.Deleted {
		status = http.StatusGone
	}
	s.renderHTML(w, status, postTemplate, s.pageData([]*Entry{e}))
}

func (s *Server) handleProfile(w http.ResponseWriter, r *http.Request) {
	entries, _, err := s.store.ListEntries(nil, defaultPageSize)
	if err != nil {
		s.serverError(w, err)
		return
	}
	var visible []*Entry
	for _, e := range entries {
		if !e.Deleted {
			visible = append(visible, e)
		}
	}
	s.renderHTML(w, http.StatusOK, profileTemplate, s.pageData(visible))
}

func (s *Server) handleKeys(w http.ResponseWriter, r *http.Request) {
	pub := s.key.Public().(ed25519.PublicKey)
	doc := sign.KeyDoc{Keys: []sign.Key{sign.NewKey(s.cfg.Signing.KeyID, pub, s.cfg.Signing.KeyCreated)}}
	writeJSON(w, http.StatusOK, doc)
}

// --- API ---

type inReplyTo struct {
	Ref  string `json:"ref"`
	Href string `json:"href"`
}

type createEntryRequest struct {
	Content     string     `json:"content"`
	ContentType string     `json:"content_type"`
	Title       string     `json:"title"`
	InReplyTo   *inReplyTo `json:"in_reply_to"`
}

type entryResponse struct {
	Seq         int64      `json:"seq"`
	ID          string     `json:"id"`
	URL         string     `json:"url"`
	Published   string     `json:"published"`
	Updated     string     `json:"updated"`
	Title       string     `json:"title,omitempty"`
	ContentType string     `json:"content_type"`
	Content     string     `json:"content"`
	InReplyTo   *inReplyTo `json:"in_reply_to,omitempty"`
	Deleted     bool       `json:"deleted"`
}

func (s *Server) entryResponse(e *Entry) entryResponse {
	resp := entryResponse{
		Seq: e.Seq, ID: e.ID, URL: s.postURL(e.Seq),
		Published: e.Published, Updated: e.Updated,
		Title: e.Title, ContentType: e.ContentType, Content: e.Content,
		Deleted: e.Deleted,
	}
	if e.InReplyToRef != "" {
		resp.InReplyTo = &inReplyTo{Ref: e.InReplyToRef, Href: e.InReplyToHref}
	}
	return resp
}

func (s *Server) handleCreateEntry(w http.ResponseWriter, r *http.Request) {
	var req createEntryRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.ContentType == "" {
		req.ContentType = "text"
	}
	if req.ContentType != "text" && req.ContentType != "html" {
		writeError(w, http.StatusBadRequest, `content_type must be "text" or "html"`)
		return
	}
	if req.Content == "" {
		writeError(w, http.StatusBadRequest, "content is required")
		return
	}
	if req.InReplyTo != nil && req.InReplyTo.Ref == "" {
		writeError(w, http.StatusBadRequest, "in_reply_to.ref is required")
		return
	}

	now := s.now().UTC()
	nowStr := now.Format(time.RFC3339)
	e, err := s.store.CreateEntry(func(seq int64) (*Entry, error) {
		e := &Entry{
			Seq:         seq,
			ID:          fmt.Sprintf("%s%s:%spost-%d", s.idPrefix, now.Format("2006-01"), s.idPath, seq),
			Published:   nowStr,
			Updated:     nowStr,
			Title:       req.Title,
			ContentType: req.ContentType,
			Content:     req.Content,
		}
		if req.InReplyTo != nil {
			e.InReplyToRef = req.InReplyTo.Ref
			e.InReplyToHref = req.InReplyTo.Href
		}
		return e, s.sign(e)
	})
	if err != nil {
		s.serverError(w, err)
		return
	}
	w.Header().Set("Location", s.postURL(e.Seq))
	writeJSON(w, http.StatusCreated, s.entryResponse(e))
	s.announce(e)
}

// announce tells others about a new or changed entry: WebSub subscribers get
// the feed, and the post a reply answers gets a WebMention.
func (s *Server) announce(e *Entry) {
	s.background(s.publish)
	if e.InReplyToHref != "" {
		source, target := s.postURL(e.Seq), e.InReplyToHref
		s.background(func() {
			if err := webmention.Deliver(s.client, source, target); err != nil {
				log.Printf("webmention %s -> %s: %v", source, target, err)
			}
		})
	}
}

func (s *Server) handleListEntries(w http.ResponseWriter, r *http.Request) {
	before, ok := cursorParam(w, r)
	if !ok {
		return
	}
	limit := int64(defaultPageSize)
	if v := r.URL.Query().Get("limit"); v != "" {
		var err error
		limit, err = strconv.ParseInt(v, 10, 64)
		if err != nil || limit < 1 || limit > maxPageSize {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("limit must be between 1 and %d", maxPageSize))
			return
		}
	}
	entries, next, err := s.store.ListEntries(before, int(limit))
	if err != nil {
		s.serverError(w, err)
		return
	}
	resp := struct {
		Entries []entryResponse `json:"entries"`
		Next    string          `json:"next,omitempty"` // cursor for ?before=
	}{Entries: []entryResponse{}}
	for _, e := range entries {
		resp.Entries = append(resp.Entries, s.entryResponse(e))
	}
	if next != nil {
		resp.Next = next.String()
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleDeleteEntry turns the entry into a tombstone: content and title are
// cleared, the id is kept, and the entry is re-signed.
func (s *Server) handleDeleteEntry(w http.ResponseWriter, r *http.Request) {
	e, ok := s.entryFromPath(w, r)
	if !ok {
		return
	}
	if !e.Deleted {
		e.Title = ""
		e.ContentType = "text"
		e.Content = ""
		e.Updated = s.now().UTC().Format(time.RFC3339)
		e.Deleted = true
		if err := s.sign(e); err != nil {
			s.serverError(w, err)
			return
		}
		if err := s.store.UpdateEntry(e); err != nil {
			s.serverError(w, err)
			return
		}
		s.announce(e)
	}
	w.WriteHeader(http.StatusNoContent)
}

type createTokenRequest struct {
	Name    string     `json:"name"`
	Expires *time.Time `json:"expires"`
}

func (s *Server) handleCreateToken(w http.ResponseWriter, r *http.Request) {
	var req createTokenRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	token, hash := GenerateToken()
	if err := s.store.CreateToken(req.Name, hash, false, req.Expires); err != nil {
		s.serverError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"token": token, "name": req.Name, "expires": req.Expires})
}

// --- login challenges ---

// handlePutChallenge publishes a login challenge for an aggregator. The value
// is stored as is; amfeed doesn't interpret it.
func (s *Server) handlePutChallenge(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	if !validChallengeToken(token) {
		writeError(w, http.StatusBadRequest, "challenge token must be 1 to 128 base64url characters")
		return
	}
	body := make([]byte, maxChallengeValue+1)
	n, _ := io.ReadFull(r.Body, body)
	value := string(body[:n])
	if n == 0 || n > maxChallengeValue || !printableASCII(value) {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("challenge value must be 1 to %d printable ASCII characters", maxChallengeValue))
		return
	}
	now := s.now()
	s.mu.Lock()
	for t, c := range s.challenges {
		if !now.Before(c.expires) {
			delete(s.challenges, t)
		}
	}
	s.challenges[token] = challenge{value: value, expires: now.Add(challengeTTL)}
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleGetChallenge(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	c, ok := s.challenges[r.PathValue("token")]
	s.mu.Unlock()
	if !ok || !s.now().Before(c.expires) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	io.WriteString(w, c.value)
}

func validChallengeToken(t string) bool {
	if len(t) == 0 || len(t) > 128 {
		return false
	}
	for _, c := range t {
		if !('a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func printableASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

// --- helpers ---

func (s *Server) requireToken(admin bool, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := web.BearerToken(r)
		if !ok {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeError(w, http.StatusUnauthorized, "bearer token required")
			return
		}
		isAdmin, err := s.store.CheckToken(web.HashToken(token))
		if errors.Is(err, ErrNotFound) {
			w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
			writeError(w, http.StatusUnauthorized, "invalid or expired token")
			return
		}
		if err != nil {
			s.serverError(w, err)
			return
		}
		if admin && !isAdmin {
			writeError(w, http.StatusForbidden, "admin token required")
			return
		}
		h(w, r)
	}
}

func (s *Server) entryFromPath(w http.ResponseWriter, r *http.Request) (*Entry, bool) {
	seq, err := strconv.ParseInt(r.PathValue("seq"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return nil, false
	}
	e, err := s.store.GetEntry(seq)
	if errors.Is(err, ErrNotFound) {
		http.NotFound(w, r)
		return nil, false
	}
	if err != nil {
		s.serverError(w, err)
		return nil, false
	}
	return e, true
}

// cursorParam parses the optional ?before= cursor. It writes a 400 and
// returns ok = false if the cursor is malformed.
func cursorParam(w http.ResponseWriter, r *http.Request) (before *paging.Cursor, ok bool) {
	v := r.URL.Query().Get("before")
	if v == "" {
		return nil, true
	}
	c, err := paging.ParseCursor(v)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return nil, false
	}
	return &c, true
}

var (
	decodeJSON = web.DecodeJSON
	writeJSON  = web.WriteJSON
	writeError = web.WriteError
)

func (s *Server) serverError(w http.ResponseWriter, err error) { web.ServerError(w, err) }
