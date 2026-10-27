package amagg

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"atomicmicro.blog/internal/atom"
	"atomicmicro.blog/internal/island"
	"atomicmicro.blog/internal/paging"
	"atomicmicro.blog/internal/web"
)

const (
	timelinePageSize = 50
	// Atom requires feed/updated; an empty timeline has never been updated.
	emptyFeedUpdated = "1970-01-01T00:00:00Z"
)

// handleIslandTimeline serves entries from all members' feeds.
func (s *Server) handleIslandTimeline(w http.ResponseWriter, r *http.Request) {
	if !s.checkPublic(w, r, true) {
		return
	}
	s.serveTimeline(w, r, 0, s.cfg.Aggregator.BaseURL+"/timeline.atom", s.cfg.Aggregator.Name)
}

// handleUserTimeline serves a member's timeline: the island plus their
// follows. Only that member may read it.
func (s *Server) handleUserTimeline(w http.ResponseWriter, r *http.Request) {
	name, ok := strings.CutSuffix(r.PathValue("file"), ".atom")
	if !ok {
		http.NotFound(w, r)
		return
	}
	u := s.checkUser(w, r, true)
	if u == nil {
		return
	}
	if u.Name != name {
		web.WriteError(w, http.StatusForbidden, "this is another member's timeline")
		return
	}
	s.serveTimeline(w, r, u.ID, s.timelineURL(u), s.cfg.Aggregator.Name+" · "+u.Name)
}

// serveTimeline serves a page of a timeline (RFC 5005 paged feed), newest
// first by published time.
func (s *Server) serveTimeline(w http.ResponseWriter, r *http.Request, userID int64, selfURL, title string) {
	var before *paging.Cursor
	if v := r.URL.Query().Get("before"); v != "" {
		c, err := paging.ParseCursor(v)
		if err != nil {
			web.WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
		before = &c
	}
	entries, next, err := s.store.Timeline(userID, before, timelinePageSize)
	if err != nil {
		web.ServerError(w, err)
		return
	}
	pageURL := func(c *paging.Cursor) string {
		if c == nil {
			return selfURL
		}
		return selfURL + "?before=" + url.QueryEscape(c.String())
	}
	f := &atom.Feed{
		ID:      selfURL,
		Title:   title,
		Updated: emptyFeedUpdated,
		Links: []atom.Link{
			{Rel: "self", Href: pageURL(before), Type: atom.MediaType},
			{Rel: "first", Href: selfURL, Type: atom.MediaType},
			{Rel: "alternate", Href: s.cfg.Aggregator.BaseURL + "/", Type: "text/html"},
		},
	}
	if next != nil {
		f.Links = append(f.Links, atom.Link{Rel: "next", Href: pageURL(next), Type: atom.MediaType})
	}
	feeds := map[int64]*island.Feed{}
	for _, e := range entries {
		ae, err := s.reserve(e, feeds)
		if err != nil {
			web.ServerError(w, err)
			return
		}
		f.Entries = append(f.Entries, ae)
		f.Updated = max(f.Updated, e.Updated)
	}
	island.WriteAtom(w, r, f)
}

// reserve prepares a stored entry for serving from this aggregator: its own
// URL as alternate, via pointing where the entry came from, and atom:source
// with the original feed. None of these are signed. feeds caches feed rows
// across calls.
func (s *Server) reserve(e *island.Entry, feeds map[int64]*island.Feed) (*atom.Entry, error) {
	doc, err := atom.Parse([]byte(e.XML))
	if err != nil || len(doc.Entries) != 1 {
		return nil, fmt.Errorf("stored entry %d: %v", e.N, err)
	}
	ae := doc.Entries[0]
	via := island.LinkHref(ae.Links, "alternate")
	if via == "" {
		via = e.FetchedFrom
	}
	links := []atom.Link{{Rel: "alternate", Type: "text/html", Href: s.postURL(e.N)}}
	for _, l := range ae.Links {
		if l.Rel != "alternate" && l.Rel != "" && l.Rel != "via" {
			links = append(links, l)
		}
	}
	ae.Links = append(links, atom.Link{Rel: "via", Href: via})

	// Our count replaces any count from upstream.
	replies, err := s.store.CountReplies(e.ID)
	if err != nil {
		return nil, err
	}
	ae.Total = strconv.Itoa(replies)

	// An entry relayed by another aggregator already names its original feed.
	if ae.Source == nil && e.FeedID.Valid {
		f, ok := feeds[e.FeedID.Int64]
		if !ok {
			if f, err = s.store.GetFeed(e.FeedID.Int64); err != nil {
				return nil, err
			}
			feeds[f.ID] = f
		}
		id := f.AtomID
		if id == "" {
			id = f.URL
		}
		ae.Source = &atom.Source{ID: id, Title: f.Title, Links: []atom.Link{{Rel: "self", Href: f.URL, Type: atom.MediaType}}}
	}
	return ae, nil
}

func (s *Server) entryFromPath(w http.ResponseWriter, r *http.Request) (*island.Entry, bool) {
	n, err := strconv.ParseInt(r.PathValue("n"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return nil, false
	}
	e, err := s.store.EntryByN(n)
	if errors.Is(err, ErrNotFound) {
		http.NotFound(w, r)
		return nil, false
	}
	if err != nil {
		web.ServerError(w, err)
		return nil, false
	}
	return e, true
}

// handlePost serves one entry: Atom if asked for, otherwise an HTML page
// showing it within its thread.
func (s *Server) handlePost(w http.ResponseWriter, r *http.Request) {
	if !s.checkPublic(w, r, false) {
		return
	}
	e, ok := s.entryFromPath(w, r)
	if !ok {
		return
	}
	w.Header().Set("Link", "<"+s.island.WebMentionURL()+`>; rel="webmention"`)
	feeds := map[int64]*island.Feed{}
	if strings.Contains(r.Header.Get("Accept"), atom.MediaType) {
		ae, err := s.reserve(e, feeds)
		if err != nil {
			web.ServerError(w, err)
			return
		}
		body, err := atom.MarshalEntry(ae)
		if err != nil {
			web.ServerError(w, err)
			return
		}
		w.Header().Set("Content-Type", atom.EntryMediaType+"; charset=utf-8")
		w.Write(body)
		return
	}
	items, err := s.island.Thread(e)
	if err != nil {
		web.ServerError(w, err)
		return
	}
	page := threadPage{Aggregator: s.cfg.Aggregator.Name, ThreadURL: s.postURL(e.N) + "/thread.atom", WebMention: s.island.WebMentionURL()}
	for _, it := range items {
		ae, err := s.reserve(it.Entry, feeds)
		if err != nil {
			web.ServerError(w, err)
			return
		}
		p := s.postView(it.Entry, ae)
		p.Depth, p.Current = min(it.Depth, 8), it.Entry.N == e.N
		if p.Current {
			page.Title = p.AuthorName
			if p.Title != "" {
				page.Title = p.Title
			}
		}
		page.Posts = append(page.Posts, p)
	}
	s.renderHTML(w, postTemplate, page)
}

// handleThread serves the whole conversation around a post as an Atom feed,
// in conversation order.
func (s *Server) handleThread(w http.ResponseWriter, r *http.Request) {
	if !s.checkPublic(w, r, true) {
		return
	}
	e, ok := s.entryFromPath(w, r)
	if !ok {
		return
	}
	items, err := s.island.Thread(e)
	if err != nil {
		web.ServerError(w, err)
		return
	}
	selfURL := s.postURL(e.N) + "/thread.atom"
	f := &atom.Feed{
		ID:      selfURL,
		Title:   "Thread · " + s.cfg.Aggregator.Name,
		Updated: emptyFeedUpdated,
		Links: []atom.Link{
			{Rel: "self", Href: selfURL, Type: atom.MediaType},
			{Rel: "alternate", Href: s.postURL(e.N), Type: "text/html"},
		},
	}
	feeds := map[int64]*island.Feed{}
	for _, it := range items {
		ae, err := s.reserve(it.Entry, feeds)
		if err != nil {
			web.ServerError(w, err)
			return
		}
		f.Entries = append(f.Entries, ae)
		f.Updated = max(f.Updated, it.Entry.Updated)
	}
	island.WriteAtom(w, r, f)
}

func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
	s.renderHTML(w, homeTemplate, map[string]any{
		"Name":     s.cfg.Aggregator.Name,
		"Public":   s.cfg.Aggregator.Public,
		"Timeline": s.cfg.Aggregator.BaseURL + "/timeline.atom",
	})
}
