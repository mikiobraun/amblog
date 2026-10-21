// Package amfeed implements the feed server: it stores your posts, signs
// them, and serves them as Atom.
package amfeed

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"atomicmicro.blog/internal/atom"
	"atomicmicro.blog/internal/sign"
)

const (
	feedSize        = 50
	defaultPageSize = 20
	maxPageSize     = 100
	// Atom requires feed/updated; an empty feed has never been updated.
	emptyFeedUpdated = "1970-01-01T00:00:00Z"
)

type Server struct {
	cfg   *Config
	store *Store
	key   ed25519.PrivateKey
	host  string // base URL hostname, used in tag: ids
	mux   *http.ServeMux
}

func NewServer(cfg *Config, store *Store, key ed25519.PrivateKey) (*Server, error) {
	u, err := url.Parse(cfg.Feed.BaseURL)
	if err != nil {
		return nil, err
	}
	s := &Server{cfg: cfg, store: store, key: key, host: u.Hostname(), mux: http.NewServeMux()}

	s.mux.HandleFunc("GET /{$}", s.handleProfile)
	s.mux.HandleFunc("GET /feed.atom", s.handleFeed)
	s.mux.HandleFunc("GET /posts/{seq}", s.handlePost)
	s.mux.HandleFunc("GET "+sign.WellKnownPath, s.handleKeys)
	s.mux.HandleFunc("POST /api/entries", s.requireToken(false, s.handleCreateEntry))
	s.mux.HandleFunc("GET /api/entries", s.requireToken(false, s.handleListEntries))
	s.mux.HandleFunc("DELETE /api/entries/{seq}", s.requireToken(false, s.handleDeleteEntry))
	s.mux.HandleFunc("POST /api/tokens", s.requireToken(true, s.handleCreateToken))
	return s, nil
}

// ServeHTTP adds CORS headers to every response. This is safe because
// authentication is bearer tokens only, never cookies.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Access-Control-Expose-Headers", "Location, ETag")
	if r.Method == http.MethodOptions {
		h.Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	s.mux.ServeHTTP(w, r)
}

// --- Atom ---

func (s *Server) feedURL() string { return s.cfg.Feed.BaseURL + "/feed.atom" }

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

func (s *Server) handleFeed(w http.ResponseWriter, r *http.Request) {
	entries, err := s.store.ListEntries(0, feedSize)
	if err != nil {
		s.serverError(w, err)
		return
	}
	updated, err := s.store.LatestUpdated()
	if err != nil {
		s.serverError(w, err)
		return
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
			{Rel: "self", Href: s.feedURL(), Type: atom.MediaType},
			{Rel: "alternate", Href: s.cfg.Feed.BaseURL + "/", Type: "text/html"},
		},
	}
	for _, e := range entries {
		f.Entries = append(f.Entries, s.atomEntry(e))
	}
	body, err := atom.MarshalFeed(f)
	if err != nil {
		s.serverError(w, err)
		return
	}
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
	entries, err := s.store.ListEntries(0, defaultPageSize)
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.renderHTML(w, http.StatusOK, profileTemplate, s.pageData(entries))
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

	now := time.Now().UTC()
	nowStr := now.Format(time.RFC3339)
	e, err := s.store.CreateEntry(func(seq int64) (*Entry, error) {
		e := &Entry{
			Seq:         seq,
			ID:          fmt.Sprintf("tag:%s,%s:post-%d", s.host, now.Format("2006-01"), seq),
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
}

func (s *Server) handleListEntries(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	before, err := intParam(q.Get("before"), 0)
	if err != nil || before < 0 {
		writeError(w, http.StatusBadRequest, "before must be a post number")
		return
	}
	limit, err := intParam(q.Get("limit"), defaultPageSize)
	if err != nil || limit < 1 || limit > maxPageSize {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("limit must be between 1 and %d", maxPageSize))
		return
	}
	entries, err := s.store.ListEntries(before, int(limit))
	if err != nil {
		s.serverError(w, err)
		return
	}
	resp := struct {
		Entries []entryResponse `json:"entries"`
	}{Entries: []entryResponse{}}
	for _, e := range entries {
		resp.Entries = append(resp.Entries, s.entryResponse(e))
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
		e.Updated = time.Now().UTC().Format(time.RFC3339)
		e.Deleted = true
		if err := s.sign(e); err != nil {
			s.serverError(w, err)
			return
		}
		if err := s.store.UpdateEntry(e); err != nil {
			s.serverError(w, err)
			return
		}
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

// --- helpers ---

func (s *Server) requireToken(admin bool, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || token == "" {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeError(w, http.StatusUnauthorized, "bearer token required")
			return
		}
		isAdmin, err := s.store.CheckToken(HashToken(token))
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

func intParam(s string, def int64) (int64, error) {
	if s == "" {
		return def, nil
	}
	return strconv.ParseInt(s, 10, 64)
}

func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func (s *Server) serverError(w http.ResponseWriter, err error) {
	log.Printf("internal error: %v", err)
	writeError(w, http.StatusInternalServerError, "internal error")
}
