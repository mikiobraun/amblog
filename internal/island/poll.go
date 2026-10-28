package island

import (
	"context"
	"fmt"
	"log"
	"net/http"
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
func (is *Island) RunPoller(ctx context.Context) {
	t := time.NewTicker(pollTick)
	defer t.Stop()
	for {
		is.PollDue()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// PollDue polls the feeds whose next poll is due.
func (is *Island) PollDue() {
	feeds, err := is.Store.DueFeeds(paging.FormatTime(is.Now()), pollBatch)
	if err != nil {
		log.Printf("poll: %v", err)
		return
	}
	for _, f := range feeds {
		if err := is.PollFeed(f); err != nil {
			log.Printf("poll %s: %v", f.URL, err)
		}
	}
}

// AddFeed returns the feed with the given URL, creating it with the default
// interval, due right away.
func (is *Island) AddFeed(url string) (*Feed, error) {
	return is.Store.AddFeed(url, is.opts.DefaultInterval, paging.FormatTime(is.Now()))
}

// PollFeed fetches a feed (conditionally), ingests new and changed entries,
// following RFC 5005 next links until it reaches entries it has seen, keeps
// its WebSub subscription current, and schedules the next poll.
func (is *Island) PollFeed(f *Feed) error {
	is.feedMu.Lock()
	defer is.feedMu.Unlock()
	// A push may have moved the feed on since f was read.
	f, err := is.Store.GetFeed(f.ID)
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
	resp, err := Fetch(is.Client, f.URL, header)
	switch {
	case err != nil:
	case resp.Status == http.StatusNotModified:
	case resp.Status != http.StatusOK:
		err = fmt.Errorf("status %d", resp.Status)
	default:
		var doc *atom.Feed
		if doc, err = atom.Parse(resp.Body); err == nil {
			f.ETag, f.LastModified = resp.ETag, resp.LastModified
			f.AtomID, f.Title, f.HTMLURL = doc.ID, doc.Title, LinkHref(doc.Links, "alternate")
			changed, err = is.ingestPages(f, doc)
			if serr := is.ensureSubscription(f, doc); serr != nil {
				log.Printf("websub %s: %v", f.URL, serr)
			}
		}
	}
	is.schedule(f, changed, err)
	if serr := is.Store.SaveFeed(f); serr != nil {
		return serr
	}
	return err
}

func (is *Island) ingestPages(f *Feed, doc *atom.Feed) (changed bool, err error) {
	lastSeen := f.LastSeen
	first := lastSeen == ""
	maxPages := maxPollPages
	if first {
		maxPages = firstFetchPages
	}
	// An entry dated in the future must not push last_seen past now, or real
	// updates would be skipped until then.
	nowStr := paging.FormatTime(is.Now())
	// Members' replies are forwarded to their sources.
	member, err := is.Store.IsMemberFeed(f.ID)
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
			c, err := is.ingest(e, origin{fetchedFrom: f.URL, feed: f, feedDoc: doc, forward: member})
			if err != nil {
				return changed, err
			}
			changed = changed || c
			f.LastSeen = max(f.LastSeen, min(updated, nowStr))
		}
		next := LinkHref(doc.Links, "next")
		if next != "" {
			if next, err = resolve(pageURL, next); err != nil {
				return changed, err
			}
		}
		if reachedSeen || next == "" || page >= maxPages {
			if first && !reachedSeen {
				f.BackfillNext = next // the history beyond the first pages, read on demand
			}
			return changed, nil
		}
		pageURL = next
		resp, err := Fetch(is.Client, pageURL, acceptFeed)
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

// Backfill reads the next older page of a feed's history, if there is one
// (see Feed.BackfillNext), and reports whether more remain. Applications call
// it when someone asks for posts older than those stored, so history is read
// one page per request and only as far as anyone looks. Entries go through
// ingest like any other: an older copy never replaces a newer version.
func (is *Island) Backfill(feedID int64) (more bool, err error) {
	is.feedMu.Lock()
	defer is.feedMu.Unlock()
	f, err := is.Store.GetFeed(feedID)
	if err != nil || f.BackfillNext == "" {
		return false, err
	}
	pageURL := f.BackfillNext
	resp, err := Fetch(is.Client, pageURL, acceptFeed)
	if err != nil {
		return true, err
	}
	if resp.Status != http.StatusOK {
		return true, fmt.Errorf("GET %s: status %d", pageURL, resp.Status)
	}
	doc, err := atom.Parse(resp.Body)
	if err != nil {
		return true, fmt.Errorf("%s: %w", pageURL, err)
	}
	member, err := is.Store.IsMemberFeed(f.ID)
	if err != nil {
		return true, err
	}
	for _, e := range doc.Entries {
		if _, err := is.ingest(e, origin{fetchedFrom: f.URL, feed: f, feedDoc: doc, forward: member}); err != nil {
			return true, err
		}
	}
	next := LinkHref(doc.Links, "next")
	if next != "" {
		if next, err = resolve(pageURL, next); err != nil {
			return true, err
		}
	}
	if next == pageURL {
		next = "" // a page linking itself would never end
	}
	f.BackfillNext = next
	return next != "", is.Store.SaveFeed(f)
}

// schedule adapts the feed's polling interval: shorter when it changed,
// longer when it didn't, backing off further on errors. A feed that pushes
// via WebSub is polled at the maximum interval only, as a fallback.
func (is *Island) schedule(f *Feed, changed bool, err error) {
	switch {
	case is.Pushing(f.ID):
		f.Interval = is.opts.MaxInterval
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
	f.Interval = min(max(f.Interval, is.opts.MinInterval), is.opts.MaxInterval)
	f.NextPoll = paging.FormatTime(is.Now().Add(f.Interval))
}
