package amagg

import (
	"database/sql"
	"log"
	"net/url"
	"strings"
	"time"

	"atomicmicro.blog/internal/atom"
	"atomicmicro.blog/internal/paging"
)

// origin is where an entry came from: the URL the aggregator fetched it from,
// and the feed it arrived through (nil for entries fetched another way).
type origin struct {
	fetchedFrom string
	feed        *Feed
	feedDoc     *atom.Feed // for the author fallback; may be nil
	depth       int        // missing parents fetched so far to get here
	forward     bool       // a reply from here is forwarded to its source (members' feeds, WebMentions)
}

// ingest stores an entry the way every ingest path does (see the spec,
// "Ingest and Verification"): verify if possible, record the result, and
// replace a stored version only if the new one is at least as trustworthy.
// It reports whether anything changed.
func (s *Server) ingest(e *atom.Entry, o origin) (changed bool, err error) {
	if e.ID == "" {
		log.Printf("ingest %s: skipping entry without id", o.fetchedFrom)
		return false, nil
	}
	updated, ok := normTime(e.Updated)
	if !ok {
		log.Printf("ingest %s: skipping entry %q with bad updated %q", o.fetchedFrom, e.ID, e.Updated)
		return false, nil
	}
	sortTime, ok := normTime(e.Published)
	if !ok {
		sortTime = updated
	}

	// Verify before filling in a missing author: signed entries carry their own.
	verified := s.keys.Verify(e) == nil
	fillAuthor(e, o)

	data, err := atom.MarshalEntry(e)
	if err != nil {
		return false, err
	}
	in := &Entry{
		ID:          e.ID,
		AuthorURI:   e.Author.URI,
		FetchedFrom: o.fetchedFrom,
		SortTime:    sortTime,
		Updated:     updated,
		Verified:    verified,
		XML:         string(data),
		Received:    paging.FormatTime(s.now()),
	}
	if o.feed != nil {
		in.FeedID = sql.NullInt64{Int64: o.feed.ID, Valid: true}
	}
	if e.InReplyTo != nil {
		in.InReplyToRef = e.InReplyTo.Ref
	}

	stored, err := s.store.EntryByID(e.ID)
	if err == ErrNotFound {
		inserted, err := s.store.InsertEntry(in)
		if err != nil {
			return false, err
		}
		if inserted {
			// A new reply whose parent we don't know: fetch the parent once,
			// so the thread has its context.
			if e.InReplyTo != nil && e.InReplyTo.Ref != "" {
				s.fetchParent(e.InReplyTo.Ref, e.InReplyTo.Href, o.depth+1)
			}
			s.forwardReply(e, o)
			return true, nil
		}
		// Another ingest path (a push, a WebMention) stored it meanwhile:
		// treat this copy as a newer version of that one.
		stored, err = s.store.EntryByID(e.ID)
	}
	if err != nil {
		return false, err
	}
	if !mayReplace(stored, in) {
		if stored.XML != in.XML {
			log.Printf("ingest %s: not replacing %q (stored: verified=%v author=%s from=%s; incoming: verified=%v author=%s)",
				o.fetchedFrom, e.ID, stored.Verified, stored.AuthorURI, stored.FetchedFrom, in.Verified, in.AuthorURI)
		}
		return false, nil
	}
	identical := in.XML == stored.XML && (stored.FeedID.Valid || !in.FeedID.Valid)
	if in.Updated < stored.Updated || identical {
		return false, nil // an older or identical copy
	}
	in.N = stored.N
	if !in.FeedID.Valid {
		in.FeedID = stored.FeedID // e.g. a parent fetched earlier keeps its place in timelines
	}
	if err := s.store.ReplaceEntry(in); err != nil {
		return false, err
	}
	if in.XML != stored.XML {
		s.forwardReply(e, o) // an edit or delete
	}
	return true, nil
}

// mayReplace is the replacement rule: a verified version replaces an entry of
// the same author; an unverified version replaces only an unverified entry
// fetched from the same URL; nothing unverified replaces a verified entry.
func mayReplace(stored, in *Entry) bool {
	if in.Verified {
		return stored.AuthorURI == in.AuthorURI
	}
	return !stored.Verified && stored.FetchedFrom == in.FetchedFrom
}

// fillAuthor gives an entry without author URI one: the feed's author, or
// else the feed itself.
func fillAuthor(e *atom.Entry, o origin) {
	if e.Author != nil && e.Author.URI != "" {
		return
	}
	var name, uri string
	if o.feedDoc != nil && o.feedDoc.Author != nil {
		name, uri = o.feedDoc.Author.Name, o.feedDoc.Author.URI
	}
	if uri == "" {
		uri = o.fetchedFrom
		if o.feed != nil {
			uri = o.feed.URL
		}
	}
	if e.Author != nil && e.Author.Name != "" {
		name = e.Author.Name
	}
	if name == "" && o.feedDoc != nil {
		name = o.feedDoc.Title
	}
	if name == "" {
		if u, err := url.Parse(uri); err == nil {
			name = u.Host
		}
	}
	e.Author = &atom.Person{Name: name, URI: uri}
}

// normTime parses an Atom timestamp and returns it as RFC 3339 UTC with
// second precision, which compares correctly as a string.
func normTime(s string) (string, bool) {
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(s))
	if err != nil {
		return "", false
	}
	return paging.FormatTime(t), true
}
