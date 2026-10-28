package ampress

import (
	"bytes"
	"errors"
	"html"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"

	"atomicmicro.blog/internal/atom"
	"atomicmicro.blog/internal/island"
	"atomicmicro.blog/internal/paging"
	"atomicmicro.blog/internal/web"
)

const pageSize = 20

// Post pages render feed HTML, and replies come from anywhere: pages are
// served with a CSP that forbids scripts, forms and plugins (sandbox), like
// island.PageCSP, but lets the theme's own styles and fonts load.
const pageCSP = "default-src 'none'; img-src * data:; style-src 'self' 'unsafe-inline'; font-src 'self'; sandbox"

var markdown = goldmark.New(goldmark.WithExtensions(extension.GFM))

// page is what every template gets. Fields a page doesn't use are empty.
type page struct {
	Blog  blogView
	Title string // of this page; empty on the index

	Posts   []postView // index
	NextURL string     // index: the next (older) page, if any

	Post       *postView  // post page
	Replies    []postView // post page, in conversation order
	WebMention string     // post page: the endpoint, for <link rel="webmention">

	HTML template.HTML // about page
}

type blogView struct {
	Title   string
	URL     string // base URL, without trailing slash
	FeedURL string
	About   bool // the blog has an about page
}

type postView struct {
	URL        string // the post page; empty for replies
	Title      string
	HTML       template.HTML // untrusted for replies, see pageCSP
	Text       string        // content of type text
	Excerpt    string        // plain text: the summary, or else the content's first paragraph, shortened
	Published  string        // RFC 3339
	Date       string        // e.g. "27 October 2026"
	AuthorName string
	AuthorURI  string
	Verified   bool
	From       string // host the entry was fetched from, shown for unverified replies
	Via        string // the entry's own URL
	Depth      int    // replies: depth below the post
	Replies    int    // posts: number of direct replies
}

func (s *Server) blogView() blogView {
	return blogView{
		Title:   s.cfg.Blog.Title,
		URL:     s.cfg.Blog.BaseURL,
		FeedURL: s.cfg.Blog.BaseURL + "/feed.atom",
		About:   s.cfg.Blog.About != "",
	}
}

func (s *Server) postView(e *island.Entry, reply bool) (postView, error) {
	doc, err := atom.Parse([]byte(e.XML))
	if err != nil || len(doc.Entries) != 1 {
		return postView{}, errors.New("stored entry: not a single Atom entry")
	}
	ae := doc.Entries[0]
	p := postView{
		Title:     ae.Title,
		Published: ae.Published,
		Date:      displayDate(ae.Published, e.SortTime),
		Verified:  e.Verified,
		Via:       island.LinkHref(ae.Links, "alternate"),
	}
	if ae.Author != nil {
		p.AuthorName, p.AuthorURI = ae.Author.Name, ae.Author.URI
	}
	if u, err := url.Parse(e.FetchedFrom); err == nil {
		p.From = u.Host
	}
	if ae.Content != nil {
		if ae.Content.Type == "html" {
			body := ae.Content.Body
			if reply {
				body = island.Nofollow(body)
			}
			p.HTML = template.HTML(body)
		} else {
			p.Text = ae.Content.Body
		}
	}
	p.Excerpt = excerpt(ae)
	return p, nil
}

const excerptLength = 300 // characters, cut at a word

var (
	paragraphRe = regexp.MustCompile(`(?is)<p[\s>].*?</p>`)
	tagRe       = regexp.MustCompile(`(?s)<[^>]*>`)
)

// excerpt returns an entry's summary, or else the first paragraph of its
// content, as plain text of at most excerptLength characters.
func excerpt(e *atom.Entry) string {
	s := e.Summary
	if s == "" && e.Content != nil {
		if e.Content.Type == "html" {
			s = paragraphRe.FindString(e.Content.Body)
			if s == "" {
				s = e.Content.Body
			}
			s = html.UnescapeString(tagRe.ReplaceAllString(s, ""))
		} else {
			s, _, _ = strings.Cut(strings.TrimSpace(e.Content.Body), "\n\n")
		}
	}
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > excerptLength {
		cut := string(r[:excerptLength])
		if i := strings.LastIndex(cut, " "); i > 0 {
			cut = cut[:i]
		}
		s = strings.TrimRight(cut, ",;:.—– ") + "…"
	}
	return s
}

func displayDate(published, fallback string) string {
	t, err := time.Parse(time.RFC3339, published)
	if err != nil {
		if t, err = time.Parse(time.RFC3339, fallback); err != nil {
			return ""
		}
	}
	return t.UTC().Format("2 January 2006")
}

func (s *Server) render(w http.ResponseWriter, name string, status int, data page) {
	var buf bytes.Buffer
	if err := s.theme.pages[name].Execute(&buf, data); err != nil {
		web.ServerError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", pageCSP)
	w.WriteHeader(status)
	w.Write(buf.Bytes())
}

func parseBefore(w http.ResponseWriter, r *http.Request) (*paging.Cursor, bool) {
	v := r.URL.Query().Get("before")
	if v == "" {
		return nil, true
	}
	c, err := paging.ParseCursor(v)
	if err != nil {
		web.WriteError(w, http.StatusBadRequest, err.Error())
		return nil, false
	}
	return &c, true
}

// posts returns a page of posts and the cursor of the next one. Where the
// stored posts run out but the feed has history left, it reads one more page
// of it first, and there is a next page as long as history remains.
func (s *Server) posts(before *paging.Cursor) ([]*Post, *paging.Cursor, error) {
	posts, next, err := s.store.Posts(before, pageSize)
	if err != nil || next != nil {
		return posts, next, err
	}
	f, err := s.store.GetFeed(s.feedID)
	if err != nil || f.BackfillNext == "" {
		return posts, nil, err
	}
	more := true
	if len(posts) < pageSize {
		if more, err = s.island.Backfill(s.feedID); err != nil {
			log.Printf("ampress: backfill %s: %v", f.URL, err) // the next request tries again
		}
		if posts, next, err = s.store.Posts(before, pageSize); err != nil || next != nil {
			return posts, next, err
		}
	}
	if !more || len(posts) == 0 {
		return posts, nil, nil
	}
	last := posts[len(posts)-1]
	return posts, &paging.Cursor{Time: last.SortTime, Seq: last.N}, nil
}

// handleIndex serves the newest posts, paged with ?before=.
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	before, ok := parseBefore(w, r)
	if !ok {
		return
	}
	posts, next, err := s.posts(before)
	if err != nil {
		web.ServerError(w, err)
		return
	}
	data := page{Blog: s.blogView()}
	for _, p := range posts {
		v, err := s.postView(p.Entry, false)
		if err != nil {
			web.ServerError(w, err)
			return
		}
		v.URL = s.postURL(p.Slug)
		if v.Replies, err = s.store.CountReplies(p.ID); err != nil {
			web.ServerError(w, err)
			return
		}
		data.Posts = append(data.Posts, v)
	}
	if next != nil {
		data.NextURL = s.cfg.Blog.BaseURL + "/?before=" + url.QueryEscape(next.String())
	}
	s.render(w, "index", http.StatusOK, data)
}

// handlePost serves a post with its replies, or the post as an Atom entry if
// asked for.
func (s *Server) handlePost(w http.ResponseWriter, r *http.Request) {
	p, err := s.store.PostBySlug(r.PathValue("slug"))
	if errors.Is(err, island.ErrNotFound) {
		s.render(w, "notfound", http.StatusNotFound, page{Blog: s.blogView(), Title: "Not found"})
		return
	}
	if err != nil {
		web.ServerError(w, err)
		return
	}
	if p.Deleted {
		s.render(w, "notfound", http.StatusGone, page{Blog: s.blogView(), Title: "Deleted"})
		return
	}
	w.Header().Set("Link", "<"+s.island.WebMentionURL()+`>; rel="webmention"`)
	if strings.Contains(r.Header.Get("Accept"), atom.MediaType) {
		ae, err := s.reserve(p.Entry, p.Slug)
		if err != nil {
			web.ServerError(w, err)
			return
		}
		island.WriteEntry(w, ae)
		return
	}
	items, err := s.island.Thread(p.Entry)
	if err != nil {
		web.ServerError(w, err)
		return
	}
	post, err := s.postView(p.Entry, false)
	if err != nil {
		web.ServerError(w, err)
		return
	}
	post.URL = s.postURL(p.Slug)
	data := page{Blog: s.blogView(), Title: post.Title, Post: &post, WebMention: s.island.WebMentionURL()}
	if data.Title == "" {
		data.Title = post.Date
	}
	for _, it := range items {
		if it.Entry.N == p.N {
			continue
		}
		v, err := s.postView(it.Entry, true)
		if err != nil {
			web.ServerError(w, err)
			return
		}
		v.Depth = min(it.Depth, 8)
		data.Replies = append(data.Replies, v)
	}
	post.Replies = len(data.Replies)
	s.render(w, "post", http.StatusOK, data)
}

// handleAbout renders the about file, read on every request so edits show
// right away.
func (s *Server) handleAbout(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Blog.About == "" {
		s.render(w, "notfound", http.StatusNotFound, page{Blog: s.blogView(), Title: "Not found"})
		return
	}
	src, err := os.ReadFile(s.cfg.Blog.About)
	if err != nil {
		web.ServerError(w, err)
		return
	}
	var buf bytes.Buffer
	if err := markdown.Convert(src, &buf); err != nil {
		web.ServerError(w, err)
		return
	}
	s.render(w, "about", http.StatusOK, page{Blog: s.blogView(), Title: "About", HTML: template.HTML(buf.String())})
}

// handleFeed serves the posts as a paged Atom feed (RFC 5005), newest first
// by published time.
func (s *Server) handleFeed(w http.ResponseWriter, r *http.Request) {
	before, ok := parseBefore(w, r)
	if !ok {
		return
	}
	posts, next, err := s.posts(before)
	if err != nil {
		web.ServerError(w, err)
		return
	}
	selfURL := s.cfg.Blog.BaseURL + "/feed.atom"
	pageURL := func(c *paging.Cursor) string {
		if c == nil {
			return selfURL
		}
		return selfURL + "?before=" + url.QueryEscape(c.String())
	}
	f := &atom.Feed{
		ID:      selfURL,
		Title:   s.cfg.Blog.Title,
		Updated: island.EmptyFeedUpdated,
		Links: []atom.Link{
			{Rel: "self", Href: pageURL(before), Type: atom.MediaType},
			{Rel: "first", Href: selfURL, Type: atom.MediaType},
			{Rel: "alternate", Href: s.cfg.Blog.BaseURL + "/", Type: "text/html"},
		},
	}
	if next != nil {
		f.Links = append(f.Links, atom.Link{Rel: "next", Href: pageURL(next), Type: atom.MediaType})
	}
	for _, p := range posts {
		ae, err := s.reserve(p.Entry, p.Slug)
		if err != nil {
			web.ServerError(w, err)
			return
		}
		f.Entries = append(f.Entries, ae)
		f.Updated = max(f.Updated, p.Updated)
	}
	island.WriteAtom(w, r, f)
}

// handleStatic serves the theme's static files. Fonts are fetched with CORS
// from the sandboxed pages, hence Access-Control-Allow-Origin.
func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("file")
	fsys := s.theme.static(name)
	if fi, err := fs.Stat(fsys, name); err != nil || fi.IsDir() {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Access-Control-Allow-Origin", "*")
	http.ServeFileFS(w, r, fsys, name)
}
