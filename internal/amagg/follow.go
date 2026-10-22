package amagg

import (
	"encoding/xml"
	"errors"
	"log"
	"net/http"

	"atomicmicro.blog/internal/paging"
	"atomicmicro.blog/internal/web"
)

type followedFeed struct {
	FeedURL string `json:"feed_url"`
	Title   string `json:"title"`
	HTMLURL string `json:"html_url,omitempty"`
}

// handleFollow follows any Atom feed, given its URL or a page linking it.
// A feed the aggregator doesn't poll yet is polled right away.
func (s *Server) handleFollow(w http.ResponseWriter, r *http.Request, u *User) {
	var req struct {
		FeedURL string `json:"feed_url"`
	}
	if !web.DecodeJSON(w, r, &req) {
		return
	}
	feedURL, _, err := discoverFeed(s.client, req.FeedURL)
	if err != nil {
		web.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	f, err := s.store.AddFeed(feedURL, s.cfg.Polling.DefaultInterval.Duration, paging.FormatTime(s.now()))
	if err != nil {
		web.ServerError(w, err)
		return
	}
	if err := s.store.Follow(u.ID, f.ID); err != nil {
		web.ServerError(w, err)
		return
	}
	if f.LastSeen == "" {
		if err := s.pollFeed(f); err != nil {
			log.Printf("follow %s: first poll: %v", f.URL, err)
		}
	}
	web.WriteJSON(w, http.StatusCreated, followedFeed{FeedURL: f.URL, Title: f.Title, HTMLURL: f.HTMLURL})
}

func (s *Server) handleUnfollow(w http.ResponseWriter, r *http.Request, u *User) {
	feedURL := r.URL.Query().Get("feed_url")
	if feedURL == "" {
		web.WriteError(w, http.StatusBadRequest, "feed_url is required")
		return
	}
	err := s.store.Unfollow(u.ID, feedURL)
	if errors.Is(err, ErrNotFound) {
		web.WriteError(w, http.StatusNotFound, "not following "+feedURL)
		return
	}
	if err != nil {
		web.ServerError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleFollowing(w http.ResponseWriter, r *http.Request, u *User) {
	feeds, err := s.store.Following(u.ID)
	if err != nil {
		web.ServerError(w, err)
		return
	}
	resp := struct {
		Feeds []followedFeed `json:"feeds"`
	}{Feeds: []followedFeed{}}
	for _, f := range feeds {
		resp.Feeds = append(resp.Feeds, followedFeed{FeedURL: f.URL, Title: f.Title, HTMLURL: f.HTMLURL})
	}
	web.WriteJSON(w, http.StatusOK, resp)
}

// OPML 2.0, just what a subscription list needs.
type opml struct {
	XMLName xml.Name `xml:"opml"`
	Version string   `xml:"version,attr"`
	Head    struct {
		Title   string `xml:"title"`
		OwnerID string `xml:"ownerId,omitempty"`
	} `xml:"head"`
	Outlines []opmlOutline `xml:"body>outline"`
}

type opmlOutline struct {
	Type    string `xml:"type,attr"`
	Text    string `xml:"text,attr"`
	XMLURL  string `xml:"xmlUrl,attr"`
	HTMLURL string `xml:"htmlUrl,attr,omitempty"`
}

// handleFollowingExport returns the follow list as OPML.
func (s *Server) handleFollowingExport(w http.ResponseWriter, r *http.Request, u *User) {
	feeds, err := s.store.Following(u.ID)
	if err != nil {
		web.ServerError(w, err)
		return
	}
	doc := opml{Version: "2.0"}
	doc.Head.Title = "Following on " + s.cfg.Aggregator.Name
	doc.Head.OwnerID = u.AuthorURI
	doc.Outlines = []opmlOutline{}
	for _, f := range feeds {
		text := f.Title
		if text == "" {
			text = f.URL
		}
		// type="rss" is OPML's conventional value for any feed subscription.
		doc.Outlines = append(doc.Outlines, opmlOutline{Type: "rss", Text: text, XMLURL: f.URL, HTMLURL: f.HTMLURL})
	}
	out, err := xml.MarshalIndent(doc, "", "  ")
	if err != nil {
		web.ServerError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/x-opml; charset=utf-8")
	w.Write([]byte(xml.Header))
	w.Write(out)
}
