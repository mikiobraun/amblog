package island

import (
	"errors"
	"log"
)

const (
	maxThreadDepth   = 50  // ancestors walked up to find the root
	maxThreadSize    = 500 // entries in one thread
	maxParentFetches = 5   // missing ancestors fetched for one new reply
)

// ThreadItem is an entry in conversation order, with its reply depth below
// the thread's root.
type ThreadItem struct {
	Entry *Entry
	Depth int
}

// Thread returns the conversation an entry belongs to: from its root (the
// topmost ancestor the island knows) down through all replies,
// depth-first. Threads are matched by thr:in-reply-to ref, never by href.
func (is *Island) Thread(e *Entry) ([]ThreadItem, error) {
	root := e
	seen := map[string]bool{e.ID: true}
	for range maxThreadDepth {
		if root.InReplyToRef == "" {
			break
		}
		parent, err := is.Store.EntryByID(root.InReplyToRef)
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

	var items []ThreadItem
	visited := map[string]bool{}
	var walk func(e *Entry, depth int) error
	walk = func(e *Entry, depth int) error {
		if visited[e.ID] || len(items) >= maxThreadSize {
			return nil
		}
		visited[e.ID] = true
		items = append(items, ThreadItem{Entry: e, Depth: depth})
		replies, err := is.Store.Replies(e.ID)
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

// fetchParent fetches a reply's missing parent from the in-reply-to href and
// ingests it like any other entry. The href may be an Atom entry or feed, or
// an HTML page linking a feed; the parent must carry the expected id.
func (is *Island) fetchParent(ref, href string, depth int) {
	if href == "" || depth > maxParentFetches {
		return
	}
	if _, err := is.Store.EntryByID(ref); !errors.Is(err, ErrNotFound) {
		return // known already, or a store error that ingest will hit again
	}
	_, doc, err := DiscoverFeed(is.Client, href)
	if err != nil {
		log.Printf("fetch parent %s: %v", href, err)
		return
	}
	for _, e := range doc.Entries {
		if e.ID == ref {
			if _, err := is.ingest(e, origin{fetchedFrom: href, feedDoc: doc, depth: depth}); err != nil {
				log.Printf("fetch parent %s: %v", href, err)
			}
			return
		}
	}
	log.Printf("fetch parent %s: no entry with id %q there", href, ref)
}
