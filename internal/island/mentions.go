package island

import (
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

	"atomicmicro.blog/internal/atom"
	"atomicmicro.blog/internal/paging"
	"atomicmicro.blog/internal/web"
	"atomicmicro.blog/internal/webmention"
)

// WebMentions: received for replies to the island's posts, and sent to
// forward replies to their sources. See the spec, "Conversations and Replies"
// and "Spam Control". The application routes POST /webmention to
// HandleWebMention.

var acceptEntry = http.Header{"Accept": {atom.EntryMediaType + ", " + atom.MediaType + ", text/html;q=0.5"}}

// HandleWebMention accepts a mention of one of our posts and processes it in
// the background (202 Accepted).
func (is *Island) HandleWebMention(w http.ResponseWriter, r *http.Request) {
	source, target, err := webmention.ParseRequest(r)
	if err != nil {
		web.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	post, ok, err := is.opts.Target(target.String())
	if err != nil {
		web.ServerError(w, err)
		return
	}
	if !ok {
		web.WriteError(w, http.StatusBadRequest, "target is not a post here")
		return
	}
	host := source.Hostname()
	if webmention.HostBlocked(host, is.opts.BlockHosts) {
		web.WriteError(w, http.StatusForbidden, "mentions from "+host+" are blocked")
		return
	}
	if ok, after := is.limiter.Allow(host, is.Now()); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(after.Seconds())+1))
		web.WriteError(w, http.StatusTooManyRequests, "too many mentions from "+host)
		return
	}
	w.WriteHeader(http.StatusAccepted)
	is.background(func() {
		if err := is.receiveMention(source.String(), target.String(), post); err != nil {
			log.Printf("webmention from %s: %v", source, err)
		}
	})
}

// receiveMention fetches the source and ingests the reply it carries. An
// Atom source must have an entry replying to the post (by ref or href); an
// HTML page must link to the target and becomes a minimal unverified entry.
// A source that is gone changes nothing: deletes arrive as tombstones.
func (is *Island) receiveMention(source, target string, post *Entry) error {
	resp, err := Fetch(is.Client, source, acceptEntry)
	if err != nil {
		return err
	}
	if resp.Status != http.StatusOK {
		return fmt.Errorf("GET %s: status %d", source, resp.Status)
	}
	o := origin{fetchedFrom: source, forward: true}
	if doc, err := atom.Parse(resp.Body); err == nil {
		for _, e := range doc.Entries {
			if e.InReplyTo != nil && (e.InReplyTo.Ref == post.ID || e.InReplyTo.Href == target) {
				o.feedDoc = doc
				_, err := is.ingest(e, o)
				return err
			}
		}
		return fmt.Errorf("%s has no entry replying to %s", source, target)
	}
	if !webmention.LinksTo(resp.Body, target) {
		return fmt.Errorf("%s doesn't link to %s", source, target)
	}
	_, err = is.ingest(webmention.PageEntry(source, target, post.ID, resp.Body, paging.FormatTime(is.Now())), o)
	return err
}

// forwardReply sends a WebMention for a new or changed reply to its parent's
// own URL (the parent's alternate as received), so the parent's author
// collects replies from every island. The source is the reply's own URL, not
// ours, so forwarding works for private islands too.
func (is *Island) forwardReply(e *atom.Entry, o origin) {
	if !o.forward || e.InReplyTo == nil || e.InReplyTo.Ref == "" {
		return
	}
	parent, err := is.Store.EntryByID(e.InReplyTo.Ref)
	if err != nil {
		return // unknown parent: nobody to tell
	}
	doc, err := atom.Parse([]byte(parent.XML))
	if err != nil || len(doc.Entries) != 1 {
		return
	}
	source, target := e.LinkHref("alternate"), doc.Entries[0].LinkHref("alternate")
	if source == "" || target == "" || source == target || strings.HasPrefix(target, is.opts.BaseURL+"/") {
		return
	}
	is.background(func() {
		if err := webmention.Deliver(is.Client, source, target); err != nil {
			log.Printf("forward %s -> %s: %v", source, target, err)
		}
	})
}
