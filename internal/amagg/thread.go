package amagg

import (
	"errors"
	"log"
	"net/http"

	"atomicmicro.blog/internal/atom"
	"atomicmicro.blog/internal/web"
)

const (
	maxThreadDepth   = 50  // ancestors walked up to find the root
	maxThreadSize    = 500 // entries in one thread
	maxParentFetches = 5   // missing ancestors fetched for one new reply
)

// threadItem is an entry in conversation order, with its reply depth below
// the thread's root.
type threadItem struct {
	entry *Entry
	depth int
}

// thread returns the conversation an entry belongs to: from its root (the
// topmost ancestor the aggregator knows) down through all replies,
// depth-first. Threads are matched by thr:in-reply-to ref, never by href.
func (s *Server) thread(e *Entry) ([]threadItem, error) {
	root := e
	seen := map[string]bool{e.ID: true}
	for range maxThreadDepth {
		if root.InReplyToRef == "" {
			break
		}
		parent, err := s.store.EntryByID(root.InReplyToRef)
		if errors.Is(err, ErrNotFound) {
			break
		}
		if err != nil {
			return nil, err
		}
		if seen[parent.ID] {
			break // a cycle; ids are chosen by authors
		}
		seen[parent.ID] = true
		root = parent
	}

	var items []threadItem
	visited := map[string]bool{}
	var walk func(e *Entry, depth int) error
	walk = func(e *Entry, depth int) error {
		if visited[e.ID] || len(items) >= maxThreadSize {
			return nil
		}
		visited[e.ID] = true
		items = append(items, threadItem{entry: e, depth: depth})
		replies, err := s.store.Replies(e.ID)
		if err != nil {
			return err
		}
		for _, r := range replies {
			if err := walk(r, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	return items, walk(root, 0)
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
	items, err := s.thread(e)
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
	feeds := map[int64]*Feed{}
	for _, it := range items {
		ae, err := s.reserve(it.entry, feeds)
		if err != nil {
			web.ServerError(w, err)
			return
		}
		f.Entries = append(f.Entries, ae)
		f.Updated = max(f.Updated, it.entry.Updated)
	}
	s.writeAtom(w, r, f)
}

// fetchParent fetches a reply's missing parent from the in-reply-to href and
// ingests it like any other entry. The href may be an Atom entry or feed, or
// an HTML page linking a feed; the parent must carry the expected id.
func (s *Server) fetchParent(ref, href string, depth int) {
	if href == "" || depth > maxParentFetches {
		return
	}
	if _, err := s.store.EntryByID(ref); !errors.Is(err, ErrNotFound) {
		return // known already, or a store error that ingest will hit again
	}
	_, doc, err := discoverFeed(s.client, href)
	if err != nil {
		log.Printf("fetch parent %s: %v", href, err)
		return
	}
	for _, e := range doc.Entries {
		if e.ID == ref {
			if _, err := s.ingest(e, origin{fetchedFrom: href, feedDoc: doc, depth: depth}); err != nil {
				log.Printf("fetch parent %s: %v", href, err)
			}
			return
		}
	}
	log.Printf("fetch parent %s: no entry with id %q there", href, ref)
}
