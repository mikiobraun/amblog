package ampress

import (
	"errors"
	"mime"
	"net/http"
	"regexp"
	"slices"

	"atomicmicro.blog/internal/island"
	"atomicmicro.blog/internal/paging"
	"atomicmicro.blog/internal/web"
)

// Reactions and reads are local to the blog: counted here, never federated
// (see the spec, "Reactions and Readers"). Readers are anonymous: the browser
// keeps a random token, and only its hash is stored. A read counts once per
// token; a reaction toggles.

// The token is random and chosen by the browser; the format only keeps out
// garbage.
var readerTokenRe = regexp.MustCompile(`^[A-Za-z0-9-]{16,64}$`)

type reactionCount struct {
	Emoji string `json:"emoji"`
	Count int    `json:"count"`
}

// postState is what the API returns for a post: reactions in the configured
// order, the reader's own, and the number of readers.
type postState struct {
	Reactions []reactionCount `json:"reactions"`
	Mine      []string        `json:"mine"`
	Reads     int             `json:"reads"`
}

func (s *Server) postState(n int64, reader string) (postState, error) {
	counts, mine, err := s.store.Reactions(n, reader)
	if err != nil {
		return postState{}, err
	}
	st := postState{Reactions: []reactionCount{}, Mine: mine}
	for _, emoji := range s.cfg.Blog.Reactions {
		st.Reactions = append(st.Reactions, reactionCount{Emoji: emoji, Count: counts[emoji]})
	}
	st.Reads, err = s.store.Reads(n)
	return st, err
}

// apiPost returns the post the request is about, or writes a 404.
func (s *Server) apiPost(w http.ResponseWriter, r *http.Request) (*Post, bool) {
	p, err := s.store.PostBySlug(r.PathValue("slug"))
	if errors.Is(err, island.ErrNotFound) || err == nil && p.Deleted {
		web.WriteError(w, http.StatusNotFound, "no such post")
		return nil, false
	}
	if err != nil {
		web.ServerError(w, err)
		return nil, false
	}
	return p, true
}

// readerHash checks a reader token and returns its hash; "" for no token.
func readerHash(w http.ResponseWriter, token string, required bool) (string, bool) {
	if token == "" && !required {
		return "", true
	}
	if !readerTokenRe.MatchString(token) {
		web.WriteError(w, http.StatusBadRequest, "reader: 16 to 64 letters, digits or dashes")
		return "", false
	}
	return web.HashToken(token), true
}

// decodeJSON accepts only application/json bodies. Browsers can't send those
// to another site without a CORS preflight, which ampress doesn't answer, so
// other pages can't react on their visitors' behalf.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/json" {
		web.WriteError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return false
	}
	return web.DecodeJSON(w, r, v)
}

func (s *Server) writeState(w http.ResponseWriter, n int64, reader string) {
	st, err := s.postState(n, reader)
	if err != nil {
		web.ServerError(w, err)
		return
	}
	web.WriteJSON(w, http.StatusOK, st)
}

// handleState: GET /api/posts/{slug}?reader=TOKEN
func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	p, ok := s.apiPost(w, r)
	if !ok {
		return
	}
	reader, ok := readerHash(w, r.URL.Query().Get("reader"), false)
	if !ok {
		return
	}
	s.writeState(w, p.N, reader)
}

// handleReact: POST /api/posts/{slug}/reactions {"reader", "emoji"} toggles
// the reader's reaction.
func (s *Server) handleReact(w http.ResponseWriter, r *http.Request) {
	p, ok := s.apiPost(w, r)
	if !ok {
		return
	}
	var req struct {
		Reader string `json:"reader"`
		Emoji  string `json:"emoji"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	reader, ok := readerHash(w, req.Reader, true)
	if !ok {
		return
	}
	if !slices.Contains(s.cfg.Blog.Reactions, req.Emoji) {
		web.WriteError(w, http.StatusBadRequest, "not one of this blog's reactions")
		return
	}
	if _, err := s.store.ToggleReaction(p.N, reader, req.Emoji, paging.FormatTime(s.now())); err != nil {
		web.ServerError(w, err)
		return
	}
	s.writeState(w, p.N, reader)
}

// handleRead: POST /api/posts/{slug}/reads {"reader"} counts the reader.
func (s *Server) handleRead(w http.ResponseWriter, r *http.Request) {
	p, ok := s.apiPost(w, r)
	if !ok {
		return
	}
	var req struct {
		Reader string `json:"reader"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	reader, ok := readerHash(w, req.Reader, true)
	if !ok {
		return
	}
	if err := s.store.AddRead(p.N, reader, paging.FormatTime(s.now())); err != nil {
		web.ServerError(w, err)
		return
	}
	s.writeState(w, p.N, reader)
}
