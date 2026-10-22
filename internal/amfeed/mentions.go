package amfeed

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"

	"atomicmicro.blog/internal/atom"
	"atomicmicro.blog/internal/paging"
	"atomicmicro.blog/internal/webmention"
)

// Received WebMentions become the posts' reply feeds (replies.atom). See the
// spec, "Conversations and Replies" and "Spam Control".

const maxSourceSize = 5 << 20

func (s *Server) webmentionURL() string { return s.cfg.Feed.BaseURL + "/webmention" }

// handleWebMention accepts a mention of one of our posts and processes it in
// the background, as WebMention allows (202 Accepted).
func (s *Server) handleWebMention(w http.ResponseWriter, r *http.Request) {
	source, target, err := webmention.ParseRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	seq, ok := s.postSeq(target.String())
	if !ok {
		writeError(w, http.StatusBadRequest, "target is not a post on this feed server")
		return
	}
	if _, err := s.store.GetEntry(seq); err != nil {
		writeError(w, http.StatusBadRequest, "target is not a post on this feed server")
		return
	}
	host := source.Hostname()
	if webmention.HostBlocked(host, s.cfg.WebMention.BlockHosts) {
		writeError(w, http.StatusForbidden, "mentions from "+host+" are blocked")
		return
	}
	if ok, after := s.limiter.Allow(host, s.now()); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(after.Seconds())+1))
		writeError(w, http.StatusTooManyRequests, "too many mentions from "+host)
		return
	}
	w.WriteHeader(http.StatusAccepted)
	s.background(func() {
		if err := s.receiveMention(source.String(), target.String(), seq); err != nil {
			log.Printf("webmention from %s: %v", source, err)
		}
	})
}

// postSeq returns the post number if u is one of our post URLs.
func (s *Server) postSeq(u string) (int64, bool) {
	rest, ok := strings.CutPrefix(u, s.cfg.Feed.BaseURL+"/posts/")
	if !ok {
		return 0, false
	}
	seq, err := strconv.ParseInt(rest, 10, 64)
	return seq, err == nil && seq > 0
}

// receiveMention fetches the source and records it as a reply to post seq,
// or removes an earlier record if the source is gone or no longer links here.
// An Atom source must carry an entry replying to the post; for an HTML page,
// a link to the target is enough, and the reply is recorded as a minimal
// entry pointing at the page.
func (s *Server) receiveMention(source, target string, seq int64) error {
	post, err := s.store.GetEntry(seq)
	if err != nil {
		return err
	}
	req, err := http.NewRequest("GET", source, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", atom.EntryMediaType+", "+atom.MediaType+", text/html;q=0.5")
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxSourceSize))
	if err != nil {
		return err
	}
	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		return s.store.DeleteMention(source, seq)
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("GET %s: status %d", source, resp.StatusCode)
	}

	now := paging.FormatTime(s.now())
	var reply *atom.Entry
	if doc, err := atom.Parse(body); err == nil {
		for _, e := range doc.Entries {
			if e.InReplyTo != nil && (e.InReplyTo.Ref == post.ID || e.InReplyTo.Href == target) {
				reply = e
				break
			}
		}
	} else if webmention.LinksTo(body, target) {
		reply = webmention.PageEntry(source, target, post.ID, body, now)
	}
	if reply == nil {
		return s.store.DeleteMention(source, seq)
	}
	data, err := atom.MarshalEntry(reply)
	if err != nil {
		return err
	}
	return s.store.PutMention(Mention{Source: source, Seq: seq, Updated: reply.Updated, XML: string(data), Received: now})
}

// handleReplies serves the replies a post received by WebMention (RFC 4685
// reply feed), most recently updated first.
func (s *Server) handleReplies(w http.ResponseWriter, r *http.Request) {
	e, ok := s.entryFromPath(w, r)
	if !ok {
		return
	}
	mentions, err := s.store.Mentions(e.Seq)
	if err != nil {
		s.serverError(w, err)
		return
	}
	selfURL := s.postURL(e.Seq) + "/replies.atom"
	f := &atom.Feed{
		ID:      selfURL,
		Title:   "Replies · " + s.cfg.Feed.Title,
		Updated: emptyFeedUpdated,
		Links: []atom.Link{
			{Rel: "self", Href: selfURL, Type: atom.MediaType},
			{Rel: "alternate", Href: s.postURL(e.Seq), Type: "text/html"},
		},
	}
	for _, m := range mentions {
		doc, err := atom.Parse([]byte(m.XML))
		if err != nil || len(doc.Entries) != 1 {
			s.serverError(w, fmt.Errorf("stored mention %s: %v", m.Source, err))
			return
		}
		f.Entries = append(f.Entries, doc.Entries[0])
		f.Updated = max(f.Updated, m.Updated)
	}
	body, err := atom.MarshalFeed(f)
	if err != nil {
		s.serverError(w, err)
		return
	}
	writeAtom(w, r, body)
}
