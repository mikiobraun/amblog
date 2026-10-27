package ampress

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"atomicmicro.blog/internal/amfeed"
	"atomicmicro.blog/internal/atom"
	"atomicmicro.blog/internal/sign"
)

// feedEnv is a real amfeed server.
type feedEnv struct {
	url   string // base URL; the author URI is url + "/"
	admin string
	srv   *amfeed.Server
}

func startFeed(t *testing.T, name string) *feedEnv {
	t.Helper()
	ts := httptest.NewUnstartedServer(nil)
	baseURL := "http://" + ts.Listener.Addr().String()
	configPath, admin, err := amfeed.Init(t.TempDir(), amfeed.InitOptions{
		BaseURL: baseURL, AuthorName: name, Title: name + "'s Feed", Listen: ":0",
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := amfeed.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	store, err := amfeed.OpenStore(cfg.Storage.Path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	key, err := sign.LoadPrivateKey(cfg.Signing.PrivateKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := amfeed.NewServer(cfg, store, key)
	if err != nil {
		t.Fatal(err)
	}
	ts.Config.Handler = srv
	ts.Start()
	t.Cleanup(ts.Close)
	return &feedEnv{url: baseURL, admin: admin, srv: srv}
}

type posted struct {
	Seq int64  `json:"seq"`
	ID  string `json:"id"`
	URL string `json:"url"`
}

// post publishes an entry (a JSON body for POST /api/entries).
func (f *feedEnv) post(t *testing.T, body string) posted {
	t.Helper()
	resp, data := request(t, "POST", f.url+"/api/entries", f.admin, body)
	if resp.StatusCode != 201 {
		t.Fatalf("post: %d %s", resp.StatusCode, data)
	}
	var p posted
	if err := json.Unmarshal([]byte(data), &p); err != nil {
		t.Fatal(err)
	}
	return p
}

// blogEnv is an ampress server with a controllable clock.
type blogEnv struct {
	t     *testing.T
	srv   *Server
	url   string
	clock time.Time
}

func startBlog(t *testing.T, feedURL string, configure func(*Config)) *blogEnv {
	t.Helper()
	ts := httptest.NewUnstartedServer(nil)
	baseURL := "http://" + ts.Listener.Addr().String()
	dir := t.TempDir()
	configPath, err := Init(dir, InitOptions{BaseURL: baseURL, Title: "Daily Briefs", Feed: feedURL, Listen: ":0"})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if configure != nil {
		configure(cfg)
	}
	store, err := OpenStore(cfg.Storage.Path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	srv, err := NewServer(cfg, store)
	if err != nil {
		t.Fatal(err)
	}
	env := &blogEnv{t: t, srv: srv, url: baseURL, clock: time.Now().UTC()}
	srv.now = func() time.Time { return env.clock }
	ts.Config.Handler = srv
	ts.Start()
	t.Cleanup(ts.Close)
	return env
}

// poll advances the clock past the feed's next poll and polls.
func (b *blogEnv) poll() {
	b.clock = b.clock.Add(2 * time.Hour)
	b.srv.PollDue()
}

func (b *blogEnv) get(path string, wantStatus int) (*http.Response, string) {
	b.t.Helper()
	resp, body := request(b.t, "GET", b.url+path, "", "")
	if resp.StatusCode != wantStatus {
		b.t.Fatalf("GET %s: %d, want %d: %s", path, resp.StatusCode, wantStatus, body)
	}
	return resp, body
}

func (b *blogEnv) feed() *atom.Feed {
	b.t.Helper()
	_, body := b.get("/feed.atom", 200)
	f, err := atom.Parse([]byte(body))
	if err != nil {
		b.t.Fatal(err)
	}
	return f
}

func request(t *testing.T, method, url, token, body string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp, string(data)
}

func TestBlog(t *testing.T) {
	alice, bob := startFeed(t, "Alice"), startFeed(t, "Bob")
	bobs := bob.post(t, `{"content":"Bob's post"}`)
	first := alice.post(t, `{"title":"First Brief","content":"<p>Hello <em>world</em></p>","content_type":"html"}`)
	alice.post(t, `{"content":"A short note"}`)
	alice.post(t, `{"content":"Alice replies elsewhere","in_reply_to":{"ref":"`+bobs.ID+`","href":"`+bobs.URL+`"}}`)
	alice.srv.Wait()
	blog := startBlog(t, alice.url+"/feed.atom", nil)
	blog.poll()

	// Index: the two top-level posts, newest first; the reply stays out.
	_, index := blog.get("/", 200)
	if !strings.Contains(index, `<a href="`+blog.url+`/posts/post-1">First Brief</a>`) || !strings.Contains(index, "A short note") {
		t.Errorf("index: %s", index)
	}
	if strings.Contains(index, "replies elsewhere") {
		t.Errorf("reply on the index: %s", index)
	}

	// Feed: posts re-served with our URLs, signatures intact.
	f := blog.feed()
	if len(f.Entries) != 2 {
		t.Fatalf("feed has %d entries, want 2", len(f.Entries))
	}
	keys := sign.NewKeyCache(http.DefaultClient)
	for _, e := range f.Entries {
		if err := keys.Verify(e); err != nil {
			t.Errorf("%s: %v", e.ID, err)
		}
	}
	var e *atom.Entry
	for _, fe := range f.Entries {
		if fe.ID == first.ID {
			e = fe
		}
	}
	if e == nil || e.LinkHref("alternate") != blog.url+"/posts/post-1" || e.LinkHref("via") != first.URL || e.Source == nil {
		t.Errorf("re-served entry: %+v", e)
	}

	// Post page: content, WebMention endpoint.
	resp, page := blog.get("/posts/post-1", 200)
	if !strings.Contains(page, "<h1>First Brief</h1>") || !strings.Contains(page, "Hello <em>world</em>") {
		t.Errorf("post page: %s", page)
	}
	if resp.Header.Get("Link") != "<"+blog.url+`/webmention>; rel="webmention"` || resp.Header.Get("Content-Security-Policy") != pageCSP {
		t.Errorf("headers: %v", resp.Header)
	}
	blog.get("/posts/post-3", 404) // the reply
	blog.get("/posts/nope", 404)

	// A reply to the post page arrives via WebMention, shows under the post
	// and is forwarded to the post's source.
	reply := bob.post(t, `{"content":"Nice brief!","in_reply_to":{"ref":"`+first.ID+`","href":"`+blog.url+`/posts/post-1"}}`)
	bob.srv.Wait()
	blog.srv.Wait()
	_, page = blog.get("/posts/post-1", 200)
	if !strings.Contains(page, "Nice brief!") || !strings.Contains(page, `href="`+bob.url+`/"`) || strings.Contains(page, "unverified") {
		t.Errorf("reply on the post page: %s", page)
	}
	_, index = blog.get("/", 200)
	if !strings.Contains(index, "1 reply") {
		t.Errorf("reply count on the index: %s", index)
	}
	alice.srv.Wait()
	_, replies := request(t, "GET", first.URL+"/replies.atom", "", "")
	if !strings.Contains(replies, reply.ID) {
		t.Errorf("reply not forwarded to the source: %s", replies)
	}

	// A deleted post is gone (pushed via WebSub, no poll).
	if resp, body := request(t, "DELETE", alice.url+"/api/entries/1", alice.admin, ""); resp.StatusCode != 204 {
		t.Fatalf("delete: %d %s", resp.StatusCode, body)
	}
	alice.srv.Wait()
	blog.get("/posts/post-1", 410)
	if f := blog.feed(); len(f.Entries) != 1 {
		t.Errorf("feed after delete: %d entries", len(f.Entries))
	}
}

func TestAboutAndTheme(t *testing.T) {
	alice := startFeed(t, "Alice")
	alice.post(t, `{"content":"Hi"}`)
	dir := t.TempDir()
	about := filepath.Join(dir, "about.md")
	theme := filepath.Join(dir, "theme")
	os.WriteFile(about, []byte("# About\n\nDaily *briefs*.\n"), 0o644)
	os.MkdirAll(filepath.Join(theme, "static"), 0o755)
	os.WriteFile(filepath.Join(theme, "static", "style.css"), []byte("body { color: red }"), 0o644)
	os.WriteFile(filepath.Join(theme, "index.html"), []byte(`{{define "content"}}{{range .Posts}}<p class="mine">{{.Text}}</p>{{end}}{{end}}`), 0o644)
	blog := startBlog(t, alice.url+"/feed.atom", func(c *Config) {
		c.Blog.About, c.Blog.Theme = about, theme
	})
	blog.poll()

	if _, body := blog.get("/about", 200); !strings.Contains(body, "Daily <em>briefs</em>") || !strings.Contains(body, `href="`+blog.url+`/about"`) {
		t.Errorf("about: %s", body)
	}
	// Overridden files replace the built-in ones; the rest stay.
	if _, body := blog.get("/", 200); !strings.Contains(body, `<p class="mine">Hi</p>`) || !strings.Contains(body, `class="site-title"`) {
		t.Errorf("themed index: %s", body)
	}
	if _, body := blog.get("/static/style.css", 200); body != "body { color: red }" {
		t.Errorf("themed style.css: %s", body)
	}
	if _, body := blog.get("/posts/post-1", 200); !strings.Contains(body, `class="replies"`) {
		t.Errorf("built-in post page: %s", body)
	}
	blog.get("/static/nope.css", 404)
	blog.get("/static/", 404)
}

// Switching the blog to another feed and back shows the first feed's posts
// again, although polling it brings nothing new.
func TestFeedSwitch(t *testing.T) {
	alice, bob := startFeed(t, "Alice"), startFeed(t, "Bob")
	alice.post(t, `{"content":"Alice's post"}`)
	bob.post(t, `{"content":"Bob's post"}`)
	blog := startBlog(t, alice.url+"/feed.atom", nil)
	blog.poll()

	restart := func(feedURL string) {
		cfg := *blog.srv.cfg
		cfg.Blog.Feed = feedURL
		srv, err := NewServer(&cfg, blog.srv.store)
		if err != nil {
			t.Fatal(err)
		}
		srv.now = func() time.Time { return blog.clock }
		blog.srv = srv
	}
	posts := func() []string {
		var out []string
		for _, e := range blog.srv.store.mustPosts(t) {
			out = append(out, e.Slug)
		}
		return out
	}
	restart(bob.url + "/feed.atom")
	blog.poll()
	if got := posts(); len(got) != 1 {
		t.Fatalf("posts after switching to bob: %v", got)
	}
	restart(alice.url + "/feed.atom")
	if got := posts(); len(got) != 1 {
		t.Fatalf("posts after switching back to alice: %v", got)
	}
	p, err := blog.srv.store.PostBySlug("post-1")
	if err != nil || p.AuthorURI != alice.url+"/" {
		t.Errorf("post-1 after switching back: %+v, %v", p, err)
	}
}

func (s *Store) mustPosts(t *testing.T) []*Post {
	t.Helper()
	posts, _, err := s.Posts(nil, pageSize)
	if err != nil {
		t.Fatal(err)
	}
	return posts
}

func TestExcerpt(t *testing.T) {
	long := strings.Repeat("word ", 100)
	for _, c := range []struct {
		entry atom.Entry
		want  string
	}{
		{atom.Entry{Summary: "The summary", Content: &atom.Content{Body: "Content"}}, "The summary"},
		{atom.Entry{Content: &atom.Content{Type: "html", Body: "<h2>Headlines</h2><ul><li>x</li></ul><p>First &amp; <em>best</em>\nparagraph.</p><p>Second.</p>"}}, "First & best paragraph."},
		{atom.Entry{Content: &atom.Content{Type: "html", Body: "<div>No <b>paragraph</b></div>"}}, "No paragraph"},
		{atom.Entry{Content: &atom.Content{Body: "First lines\nof text.\n\nSecond."}}, "First lines of text."},
		{atom.Entry{Content: &atom.Content{Body: long}}, strings.TrimSpace(strings.Repeat("word ", 59)) + " word…"},
		{atom.Entry{}, ""},
	} {
		if got := excerpt(&c.entry); got != c.want {
			t.Errorf("excerpt(%+v) = %q, want %q", c.entry.Content, got, c.want)
		}
	}
}

func TestSlugs(t *testing.T) {
	for id, want := range map[string]string{
		"tag:miki.one,2026-10:content/2026-10-27": "2026-10-27",
		"tag:miki.one,2024-12:post-42":            "post-42",
		"tag:localhost,2026-10:8100/post-7":       "post-7",
		"https://example.com/2026/hello":          "hello",
		"tag:example.com,2026:":                   "",
	} {
		if got := slugOf(id); got != want {
			t.Errorf("slugOf(%q) = %q, want %q", id, got, want)
		}
	}

	store, err := OpenStore(filepath.Join(t.TempDir(), "ampress.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	db := store.DB()
	db.Exec(`INSERT INTO feeds (id, url, interval_s, next_poll) VALUES (1, 'x', 60, '')`)
	for n := 1; n <= 2; n++ {
		db.Exec(`INSERT INTO entries (n, id, author_uri, fetched_from, feed_id, in_reply_to_ref, sort_time, updated, verified, xml, received)
			VALUES (?, ?, '', '', 1, '', '', '', 0, '', '')`, n, "id-"+string(rune('0'+n)))
	}
	if err := store.SavePost(1, "same", false); err != nil {
		t.Fatal(err)
	}
	if err := store.SavePost(2, "same", false); err != errSlugTaken {
		t.Errorf("second entry with the same slug: %v", err)
	}
	if err := store.SavePost(1, "same", true); err != nil {
		t.Errorf("the slug's owner can update it: %v", err)
	}
}
