package amagg

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"time"

	"atomicmicro.blog/internal/atom"
	"atomicmicro.blog/internal/paging"
)

const (
	pollTick        = 10 * time.Second
	pollBatch       = 20
	firstFetchPages = 3  // history read when a feed is new
	maxPollPages    = 10 // safety limit when catching up
)

// RunPoller polls due feeds until ctx is cancelled.
func (s *Server) RunPoller(ctx context.Context) {
	t := time.NewTicker(pollTick)
	defer t.Stop()
	for {
		s.PollDue()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// PollDue polls the feeds whose next poll is due.
func (s *Server) PollDue() {
	feeds, err := s.store.DueFeeds(paging.FormatTime(s.now()), pollBatch)
	if err != nil {
		log.Printf("poll: %v", err)
		return
	}
	for _, f := range feeds {
		if err := s.pollFeed(f); err != nil {
			log.Printf("poll %s: %v", f.URL, err)
		}
	}
}

// pollFeed fetches a feed (conditionally), ingests new and changed entries,
// following RFC 5005 next links until it reaches entries it has seen, keeps
// its WebSub subscription current, and schedules the next poll.
func (s *Server) pollFeed(f *Feed) error {
	s.feedMu.Lock()
	defer s.feedMu.Unlock()
	// A push may have moved the feed on since f was read.
	f, err := s.store.GetFeed(f.ID)
	if err != nil {
		return err
	}
	header := acceptFeed.Clone()
	if f.ETag != "" {
		header.Set("If-None-Match", f.ETag)
	}
	if f.LastModified != "" {
		header.Set("If-Modified-Since", f.LastModified)
	}
	changed := false
	resp, err := fetch(s.client, f.URL, header)
	switch {
	case err != nil:
	case resp.Status == http.StatusNotModified:
	case resp.Status != http.StatusOK:
		err = fmt.Errorf("status %d", resp.Status)
	default:
		var doc *atom.Feed
		if doc, err = atom.Parse(resp.Body); err == nil {
			f.ETag, f.LastModified = resp.ETag, resp.LastModified
			f.AtomID, f.Title, f.HTMLURL = doc.ID, doc.Title, linkHref(doc.Links, "alternate")
			changed, err = s.ingestPages(f, doc)
			if serr := s.ensureSubscription(f, doc); serr != nil {
				log.Printf("websub %s: %v", f.URL, serr)
			}
		}
	}
	s.schedule(f, changed, err)
	if serr := s.store.SaveFeed(f); serr != nil {
		return serr
	}
	return err
}

func (s *Server) ingestPages(f *Feed, doc *atom.Feed) (changed bool, err error) {
	lastSeen := f.LastSeen
	maxPages := maxPollPages
	if lastSeen == "" {
		maxPages = firstFetchPages
	}
	// An entry dated in the future must not push last_seen past now, or real
	// updates would be skipped until then.
	nowStr := paging.FormatTime(s.now())
	// Members' replies are forwarded to their sources.
	member, err := s.store.IsMemberFeed(f.ID)
	if err != nil {
		return false, err
	}
	pageURL := f.URL
	for page := 1; ; page++ {
		reachedSeen := false
		for _, e := range doc.Entries {
			updated, _ := normTime(e.Updated)
			// Equal timestamps are re-checked: they have second precision.
			if lastSeen != "" && updated != "" && updated < lastSeen {
				reachedSeen = true
				continue
			}
			c, err := s.ingest(e, origin{fetchedFrom: f.URL, feed: f, feedDoc: doc, forward: member})
			if err != nil {
				return changed, err
			}
			changed = changed || c
			f.LastSeen = max(f.LastSeen, min(updated, nowStr))
		}
		next := linkHref(doc.Links, "next")
		if reachedSeen || next == "" || page >= maxPages {
			return changed, nil
		}
		if pageURL, err = resolve(pageURL, next); err != nil {
			return changed, err
		}
		resp, err := fetch(s.client, pageURL, acceptFeed)
		if err != nil {
			return changed, err
		}
		if resp.Status != http.StatusOK {
			return changed, fmt.Errorf("GET %s: status %d", pageURL, resp.Status)
		}
		if doc, err = atom.Parse(resp.Body); err != nil {
			return changed, fmt.Errorf("%s: %w", pageURL, err)
		}
	}
}

// schedule adapts the feed's polling interval: shorter when it changed,
// longer when it didn't, backing off further on errors. A feed that pushes
// via WebSub is polled at max_interval only, as a fallback.
func (s *Server) schedule(f *Feed, changed bool, err error) {
	p := s.cfg.Polling
	switch {
	case s.pushing(f.ID):
		f.Interval = p.MaxInterval.Duration
		if err != nil {
			f.Failures++
			f.LastError = err.Error()
		} else {
			f.Failures, f.LastError = 0, ""
		}
	case err != nil:
		f.Failures++
		f.LastError = err.Error()
		f.Interval *= 2
	case changed:
		f.Failures, f.LastError = 0, ""
		f.Interval /= 2
	default:
		f.Failures, f.LastError = 0, ""
		f.Interval = f.Interval * 3 / 2
	}
	f.Interval = min(max(f.Interval, p.MinInterval.Duration), p.MaxInterval.Duration)
	f.NextPoll = paging.FormatTime(s.now().Add(f.Interval))
}

func linkHref(links []atom.Link, rel string) string {
	for _, l := range links {
		if l.Rel == rel || (l.Rel == "" && rel == "alternate") {
			return l.Href
		}
	}
	return ""
}

func resolve(base, ref string) (string, error) {
	b, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	r, err := url.Parse(ref)
	if err != nil {
		return "", err
	}
	return b.ResolveReference(r).String(), nil
}
