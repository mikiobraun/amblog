package amagg

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
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
	return &feedEnv{url: baseURL, admin: admin}
}

// post publishes an entry and returns its post URL.
func (f *feedEnv) post(t *testing.T, body string) string {
	t.Helper()
	resp, data := request(t, "POST", f.url+"/api/entries", f.admin, body)
	if resp.StatusCode != 201 {
		t.Fatalf("post: %d %s", resp.StatusCode, data)
	}
	return resp.Header.Get("Location")
}

// aggEnv is an amagg server with a controllable clock.
type aggEnv struct {
	t     *testing.T
	srv   *Server
	url   string
	clock time.Time
}

func startAgg(t *testing.T, configure func(*Config)) *aggEnv {
	t.Helper()
	ts := httptest.NewUnstartedServer(nil)
	baseURL := "http://" + ts.Listener.Addr().String()
	dir := t.TempDir()
	configPath, err := Init(dir, InitOptions{BaseURL: baseURL, Name: "Capy Club", Listen: ":0"})
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
	env := &aggEnv{t: t, srv: srv, url: baseURL, clock: time.Now().UTC()}
	srv.now = func() time.Time { return env.clock }
	ts.Config.Handler = srv
	ts.Start()
	t.Cleanup(ts.Close)
	return env
}

func (a *aggEnv) invite() string {
	a.t.Helper()
	code, err := CreateInvite(a.srv.store)
	if err != nil {
		a.t.Fatal(err)
	}
	return code
}

// login runs the login flow the way a client does and returns the session.
func (a *aggEnv) login(f *feedEnv, invite string) loginResponse {
	a.t.Helper()
	resp, body := a.tryLogin(f, invite, a.url)
	if resp.StatusCode != 200 {
		a.t.Fatalf("login: %d %s", resp.StatusCode, body)
	}
	var lr loginResponse
	if err := json.Unmarshal([]byte(body), &lr); err != nil {
		a.t.Fatal(err)
	}
	return lr
}

// tryLogin publishes the challenge bound to origin (normally the
// aggregator's own) and returns the verify response.
func (a *aggEnv) tryLogin(f *feedEnv, invite, origin string) (*http.Response, string) {
	a.t.Helper()
	req, _ := json.Marshal(loginRequest{AuthorURI: f.url + "/", Invite: invite})
	resp, body := request(a.t, "POST", a.url+"/api/login", "", string(req))
	if resp.StatusCode != 200 {
		return resp, body
	}
	var ch struct{ Token string }
	json.Unmarshal([]byte(body), &ch)
	resp, body = request(a.t, "PUT", f.url+"/api/challenges/"+ch.Token, f.admin, ch.Token+"."+origin)
	if resp.StatusCode != 204 {
		a.t.Fatalf("publish challenge: %d %s", resp.StatusCode, body)
	}
	return request(a.t, "POST", a.url+"/api/login/verify", "", `{"token":"`+ch.Token+`"}`)
}

func (a *aggEnv) feed(path, token string) *atom.Feed {
	a.t.Helper()
	resp, body := request(a.t, "GET", a.url+path, token, "")
	if resp.StatusCode != 200 {
		a.t.Fatalf("GET %s: %d %s", path, resp.StatusCode, body)
	}
	f, err := atom.Parse([]byte(body))
	if err != nil {
		a.t.Fatal(err)
	}
	return f
}

// tick advances the clock past every feed's next poll and polls.
func (a *aggEnv) poll() {
	a.clock = a.clock.Add(2 * time.Hour)
	a.srv.PollDue()
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

func contents(f *atom.Feed) []string {
	var out []string
	for _, e := range f.Entries {
		out = append(out, e.Content.Body)
	}
	return out
}

func TestLogin(t *testing.T) {
	agg := startAgg(t, nil)
	alice := startFeed(t, "Alice")
	bob := startFeed(t, "Bob")

	// Invite-only by default.
	if resp, body := agg.tryLogin(alice, "", agg.url); resp.StatusCode != 403 {
		t.Fatalf("no invite: %d %s", resp.StatusCode, body)
	}
	code := agg.invite()
	// A challenge bound to another aggregator doesn't count.
	if resp, body := agg.tryLogin(alice, code, "https://evil.example"); resp.StatusCode != 403 {
		t.Fatalf("wrong origin: %d %s", resp.StatusCode, body)
	}

	lr := agg.login(alice, code)
	if lr.AuthorURI != alice.url+"/" || lr.FeedURL != alice.url+"/feed.atom" {
		t.Errorf("login response = %+v", lr)
	}
	if lr.TimelineURL != agg.url+"/timeline/"+lr.Name+".atom" {
		t.Errorf("timeline url = %q", lr.TimelineURL)
	}
	// Existing members log in without an invite; the invite is used up.
	agg.login(alice, "")
	if resp, body := agg.tryLogin(bob, code, agg.url); resp.StatusCode != 403 {
		t.Fatalf("used invite: %d %s", resp.StatusCode, body)
	}

	// Login tokens are single-use.
	resp, body := request(t, "POST", agg.url+"/api/login", "", `{"author_uri":"`+alice.url+`/"}`)
	var ch struct{ Token string }
	json.Unmarshal([]byte(body), &ch)
	request(t, "PUT", alice.url+"/api/challenges/"+ch.Token, alice.admin, ch.Token+"."+agg.url)
	request(t, "POST", agg.url+"/api/login/verify", "", `{"token":"`+ch.Token+`"}`)
	resp, body = request(t, "POST", agg.url+"/api/login/verify", "", `{"token":"`+ch.Token+`"}`)
	if resp.StatusCode != 400 {
		t.Errorf("reused login token: %d %s", resp.StatusCode, body)
	}

	if resp, body := request(t, "GET", agg.url+"/api/following", "amag_bogus", ""); resp.StatusCode != 401 {
		t.Errorf("bogus token: %d %s", resp.StatusCode, body)
	}
}

func TestOpenRegistration(t *testing.T) {
	agg := startAgg(t, func(c *Config) { c.Aggregator.Registration = "open" })
	agg.login(startFeed(t, "Alice"), "")
}

func TestTimelines(t *testing.T) {
	agg := startAgg(t, nil)
	alice, bob, carol := startFeed(t, "Alice"), startFeed(t, "Bob"), startFeed(t, "Carol")
	alice.post(t, `{"content":"alice 1"}`)
	bob.post(t, `{"content":"bob 1"}`)
	carol.post(t, `{"content":"carol 1"}`)

	aliceSession := agg.login(alice, agg.invite())
	bobSession := agg.login(bob, agg.invite())
	// Alice follows Carol, who is not a member; given her profile page.
	resp, body := request(t, "POST", agg.url+"/api/follow", aliceSession.Token, `{"feed_url":"`+carol.url+`/"}`)
	if resp.StatusCode != 201 {
		t.Fatalf("follow: %d %s", resp.StatusCode, body)
	}

	island := agg.feed("/timeline.atom", "")
	if got := strings.Join(contents(island), ","); got != "bob 1,alice 1" && got != "alice 1,bob 1" {
		t.Errorf("island = %s", got)
	}
	home := agg.feed("/timeline/"+aliceSession.Name+".atom", aliceSession.Token)
	if len(home.Entries) != 3 {
		t.Errorf("alice's timeline = %v", contents(home))
	}
	if bobs := agg.feed("/timeline/"+bobSession.Name+".atom", bobSession.Token); len(bobs.Entries) != 2 {
		t.Errorf("bob's timeline = %v (should not include carol)", contents(bobs))
	}
	if resp, _ := request(t, "GET", agg.url+"/timeline/"+aliceSession.Name+".atom", bobSession.Token, ""); resp.StatusCode != 403 {
		t.Errorf("bob reading alice's timeline: %d", resp.StatusCode)
	}
	if resp, _ := request(t, "GET", agg.url+"/timeline/"+aliceSession.Name+".atom", "", ""); resp.StatusCode != 401 {
		t.Errorf("anonymous reading alice's timeline: %d", resp.StatusCode)
	}

	// Re-served entries: own alternate, via and source, signature intact.
	keys := sign.NewKeyCache(http.DefaultClient)
	for _, e := range home.Entries {
		if alt := e.LinkHref("alternate"); !strings.HasPrefix(alt, agg.url+"/posts/") {
			t.Errorf("%s: alternate = %q", e.ID, alt)
		}
		if via := e.LinkHref("via"); !strings.Contains(via, "/posts/1") {
			t.Errorf("%s: via = %q", e.ID, via)
		}
		if e.Source == nil || !strings.HasSuffix(e.Source.ID, "/feed.atom") {
			t.Errorf("%s: source = %+v", e.ID, e.Source)
		}
		if err := keys.Verify(e); err != nil {
			t.Errorf("%s: %v", e.ID, err)
		}
	}

	// The post page serves the re-served entry.
	alt := home.Entries[0].LinkHref("alternate")
	req, _ := http.NewRequest("GET", alt, nil)
	req.Header.Set("Accept", atom.MediaType)
	r, err := http.DefaultClient.Do(req)
	if err != nil || r.StatusCode != 200 {
		t.Fatalf("GET %s: %v %v", alt, r.Status, err)
	}
	r.Body.Close()
	resp, body = request(t, "GET", alt, "", "")
	if resp.StatusCode != 200 || !strings.Contains(body, "✓") || resp.Header.Get("Content-Security-Policy") == "" {
		t.Errorf("post page: %d %s", resp.StatusCode, body)
	}

	// Following list and unfollow.
	resp, body = request(t, "GET", agg.url+"/api/following/export", aliceSession.Token, "")
	if resp.StatusCode != 200 || !strings.Contains(body, `xmlUrl="`+carol.url+`/feed.atom"`) {
		t.Errorf("export: %d %s", resp.StatusCode, body)
	}
	resp, body = request(t, "DELETE", agg.url+"/api/follow?feed_url="+carol.url+"/feed.atom", aliceSession.Token, "")
	if resp.StatusCode != 204 {
		t.Fatalf("unfollow: %d %s", resp.StatusCode, body)
	}
	if home := agg.feed("/timeline/"+aliceSession.Name+".atom", aliceSession.Token); len(home.Entries) != 2 {
		t.Errorf("after unfollow: %v", contents(home))
	}
}

func TestPollingEditsAndDeletes(t *testing.T) {
	agg := startAgg(t, nil)
	alice := startFeed(t, "Alice")
	agg.login(alice, agg.invite())
	if n := len(agg.feed("/timeline.atom", "").Entries); n != 0 {
		t.Fatalf("empty feed: %d entries", n)
	}

	alice.post(t, `{"content":"first"}`)
	alice.post(t, `{"content":"second"}`)
	agg.poll()
	if got := contents(agg.feed("/timeline.atom", "")); len(got) != 2 {
		t.Fatalf("after poll: %v", got)
	}

	// amfeed's second-precision timestamps: wait so the delete gets a newer updated.
	time.Sleep(1100 * time.Millisecond)
	resp, body := request(t, "DELETE", alice.url+"/api/entries/1", alice.admin, "")
	if resp.StatusCode != 204 {
		t.Fatalf("delete: %d %s", resp.StatusCode, body)
	}
	agg.poll()
	got := strings.Join(contents(agg.feed("/timeline.atom", "")), "|")
	if got != "|second" && got != "second|" {
		t.Errorf("after delete: %q (want a tombstone and second)", got)
	}
}

// A plain Atom feed: unsigned entries are accepted as unverified, get an
// author, are paged through, and can't take over a verified entry.
func TestPlainFeed(t *testing.T) {
	agg := startAgg(t, nil)
	alice := startFeed(t, "Alice")
	session := agg.login(alice, agg.invite())
	alice.post(t, `{"content":"signed by alice"}`)
	agg.poll()
	aliceEntry := agg.feed("/timeline.atom", "").Entries[0]

	var mu sync.Mutex
	pages := map[string]string{}
	blog := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		page, ok := pages[r.URL.RequestURI()]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", atom.MediaType)
		io.WriteString(w, page)
	}))
	defer blog.Close()
	setPages := func(p map[string]string) {
		mu.Lock()
		defer mu.Unlock()
		pages = p
	}
	entry := func(id, updated, content string) string {
		return `<entry><id>` + id + `</id><title></title><updated>` + updated + `</updated>` +
			`<link href="` + blog.URL + `/` + id + `"/><content>` + content + `</content></entry>`
	}
	feed := func(next string, entries ...string) string {
		links := ""
		if next != "" {
			links = `<link rel="next" href="` + next + `"/>`
		}
		return `<feed xmlns="http://www.w3.org/2005/Atom"><id>urn:blog</id><title>Blog</title>` +
			`<updated>2026-10-01T00:00:00Z</updated>` + links + strings.Join(entries, "") + `</feed>`
	}
	setPages(map[string]string{
		"/feed.atom": feed("/feed.atom?page=2", entry("b2", "2026-10-02T00:00:00Z", "two")),
		"/feed.atom?page=2": feed("", entry("b1", "2026-10-01T00:00:00Z", "one"),
			// Claims Alice's entry id, unsigned: must not replace it.
			entry(aliceEntry.ID, "2030-01-01T00:00:00Z", "hijacked")),
	})
	resp, body := request(t, "POST", agg.url+"/api/follow", session.Token, `{"feed_url":"`+blog.URL+`/feed.atom"}`)
	if resp.StatusCode != 201 {
		t.Fatalf("follow: %d %s", resp.StatusCode, body)
	}

	home := agg.feed("/timeline/"+session.Name+".atom", session.Token)
	byContent := map[string]*atom.Entry{}
	for _, e := range home.Entries {
		byContent[e.Content.Body] = e
	}
	if byContent["one"] == nil || byContent["two"] == nil {
		t.Fatalf("paged blog entries missing: %v", contents(home))
	}
	if byContent["hijacked"] != nil || byContent["signed by alice"] == nil {
		t.Errorf("unsigned copy replaced a verified entry: %v", contents(home))
	}
	if a := byContent["one"].Author; a == nil || a.URI != blog.URL+"/feed.atom" || a.Name != "Blog" {
		t.Errorf("author fallback = %+v", a)
	}

	// An edit from the same URL replaces the unverified entry.
	setPages(map[string]string{
		"/feed.atom": feed("", entry("b2", agg.clock.Add(time.Hour).Format(time.RFC3339), "two, edited")),
	})
	agg.poll()
	got := contents(agg.feed("/timeline/"+session.Name+".atom", session.Token))
	if !strings.Contains(strings.Join(got, "|"), "two, edited") || strings.Contains(strings.Join(got, "|"), "|two|") {
		t.Errorf("after edit: %q", got)
	}
}

// postID returns the Atom id of a post on a feed server, given its URL.
func postID(t *testing.T, postURL string) string {
	t.Helper()
	req, _ := http.NewRequest("GET", postURL, nil)
	req.Header.Set("Accept", atom.MediaType)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	f, err := atom.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	return f.Entries[0].ID
}

func reply(t *testing.T, f *feedEnv, parentURL, text string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"content":     text,
		"in_reply_to": map[string]string{"ref": postID(t, parentURL), "href": parentURL},
	})
	return f.post(t, string(body))
}

func TestThreads(t *testing.T) {
	agg := startAgg(t, nil)
	alice, bob, dave := startFeed(t, "Alice"), startFeed(t, "Bob"), startFeed(t, "Dave")
	agg.login(alice, agg.invite())
	agg.login(bob, agg.invite())

	root := alice.post(t, `{"content":"root"}`)
	r1 := reply(t, bob, root, "bob replies")
	reply(t, alice, r1, "alice answers bob")
	// Dave is unknown to the island; Bob replies to him, so Dave's post is
	// fetched as the missing parent.
	davePost := dave.post(t, `{"content":"dave's post"}`)
	reply(t, bob, davePost, "bob replies to dave")
	agg.poll()

	island := agg.feed("/timeline.atom", "")
	var rootAlt string
	for _, e := range island.Entries {
		if e.Content.Body == "root" {
			rootAlt = e.LinkHref("alternate")
			if e.Total != "1" {
				t.Errorf("root thr:total = %q, want 1 (direct replies)", e.Total)
			}
		}
		if e.Content.Body == "alice answers bob" && e.Total != "0" {
			t.Errorf("leaf thr:total = %q, want 0", e.Total)
		}
		if e.Content.Body == "dave's post" {
			t.Error("a fetched parent must not appear in timelines")
		}
	}
	thread := agg.feed(strings.TrimPrefix(rootAlt, agg.url)+"/thread.atom", "")
	if got := strings.Join(contents(thread), " / "); got != "root / bob replies / alice answers bob" {
		t.Errorf("thread = %s", got)
	}

	// The thread of the reply to Dave includes Dave's post, fetched.
	for _, e := range island.Entries {
		if e.Content.Body == "bob replies to dave" {
			th := agg.feed(strings.TrimPrefix(e.LinkHref("alternate"), agg.url)+"/thread.atom", "")
			if got := strings.Join(contents(th), " / "); got != "dave's post / bob replies to dave" {
				t.Errorf("dave thread = %s", got)
			}
		}
	}

	// The HTML page shows the whole thread.
	resp, body := request(t, "GET", rootAlt, "", "")
	if resp.StatusCode != 200 || !strings.Contains(body, "alice answers bob") {
		t.Errorf("post page: %d %s", resp.StatusCode, body)
	}
}

func TestFeedReaders(t *testing.T) {
	agg := startAgg(t, func(c *Config) { c.Aggregator.Public = false })
	alice := startFeed(t, "Alice")
	session := agg.login(alice, agg.invite())

	resp, body := request(t, "POST", agg.url+"/api/readers", session.Token, `{"name":"NetNewsWire"}`)
	if resp.StatusCode != 201 {
		t.Fatalf("create reader: %d %s", resp.StatusCode, body)
	}
	var cred struct {
		ID       int64
		Username string
		Password string
	}
	json.Unmarshal([]byte(body), &cred)
	if cred.Username != session.Name || !strings.HasPrefix(cred.Password, "amr_") {
		t.Fatalf("credential = %+v", cred)
	}

	basic := func(path, password string) int {
		req, _ := http.NewRequest("GET", agg.url+path, nil)
		req.SetBasicAuth(cred.Username, password)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	timeline := "/timeline/" + session.Name + ".atom"
	if got := basic(timeline, cred.Password); got != 200 {
		t.Errorf("member timeline with reader password: %d", got)
	}
	if got := basic("/timeline.atom", cred.Password); got != 200 {
		t.Errorf("private island timeline with reader password: %d", got)
	}
	if got := basic(timeline, "amr_wrong"); got != 401 {
		t.Errorf("wrong password: %d", got)
	}
	if got := basic("/api/following", cred.Password); got != 401 {
		t.Errorf("reader password on the API: %d", got)
	}
	resp, _ = request(t, "GET", agg.url+timeline, "", "")
	if h := resp.Header.Values("WWW-Authenticate"); len(h) != 2 || !strings.HasPrefix(h[1], "Basic") {
		t.Errorf("WWW-Authenticate = %q", h)
	}

	resp, body = request(t, "GET", agg.url+"/api/readers", session.Token, "")
	if resp.StatusCode != 200 || !strings.Contains(body, "NetNewsWire") || strings.Contains(body, "amr_") {
		t.Errorf("list readers: %d %s", resp.StatusCode, body)
	}
	resp, _ = request(t, "DELETE", agg.url+"/api/readers/"+strconv.FormatInt(cred.ID, 10), session.Token, "")
	if resp.StatusCode != 204 {
		t.Fatalf("revoke: %d", resp.StatusCode)
	}
	if got := basic(timeline, cred.Password); got != 401 {
		t.Errorf("revoked password: %d", got)
	}
}

func TestProxy(t *testing.T) {
	agg := startAgg(t, nil)
	alice, carol := startFeed(t, "Alice"), startFeed(t, "Carol")
	session := agg.login(alice, agg.invite())
	carol.post(t, `{"content":"via the proxy"}`)

	proxy := "/api/feed?url=" + url.QueryEscape(carol.url+"/feed.atom")
	if resp, _ := request(t, "GET", agg.url+proxy, "", ""); resp.StatusCode != 401 {
		t.Errorf("proxy without token: %d", resp.StatusCode)
	}
	if f := agg.feed(proxy, session.Token); len(f.Entries) != 1 || f.Entries[0].Content.Body != "via the proxy" {
		t.Errorf("proxied feed = %v", contents(f))
	}
	// Not Atom: the profile page is HTML.
	resp, body := request(t, "GET", agg.url+"/api/feed?url="+url.QueryEscape(carol.url+"/"), session.Token, "")
	if resp.StatusCode != 502 {
		t.Errorf("proxying HTML: %d %s", resp.StatusCode, body)
	}
	resp, _ = request(t, "GET", agg.url+"/api/feed?url=file:///etc/passwd", session.Token, "")
	if resp.StatusCode != 400 {
		t.Errorf("proxying a file URL: %d", resp.StatusCode)
	}
}

func TestPrivateIsland(t *testing.T) {
	agg := startAgg(t, func(c *Config) { c.Aggregator.Public = false })
	alice := startFeed(t, "Alice")
	session := agg.login(alice, agg.invite())
	if resp, _ := request(t, "GET", agg.url+"/timeline.atom", "", ""); resp.StatusCode != 401 {
		t.Errorf("anonymous on private island: %d", resp.StatusCode)
	}
	agg.feed("/timeline.atom", session.Token)
}

func TestMayReplace(t *testing.T) {
	v := func(author, from string) *Entry { return &Entry{Verified: true, AuthorURI: author, FetchedFrom: from} }
	u := func(author, from string) *Entry { return &Entry{AuthorURI: author, FetchedFrom: from} }
	for _, c := range []struct {
		name       string
		stored, in *Entry
		mayReplace bool
	}{
		{"unverified → verified, same author", u("a", "x"), v("a", "y"), true},
		{"unverified → verified, other author", u("a", "x"), v("b", "x"), false},
		{"verified → verified, same author", v("a", "x"), v("a", "y"), true},
		{"verified → verified, other author", v("a", "x"), v("b", "x"), false},
		{"unverified → unverified, same source", u("a", "x"), u("b", "x"), true},
		{"unverified → unverified, other source", u("a", "x"), u("a", "y"), false},
		{"verified → unverified", v("a", "x"), u("a", "x"), false},
	} {
		if got := mayReplace(c.stored, c.in); got != c.mayReplace {
			t.Errorf("%s: got %v", c.name, got)
		}
	}
}

func TestFindFeedLink(t *testing.T) {
	page := `<html><head><link rel="stylesheet" href="/s.css">
<LINK Type='application/atom+xml' rel="alternate home" href="/feed.atom?a=1&amp;b=2"></head></html>`
	if got := findFeedLink([]byte(page)); got != "/feed.atom?a=1&b=2" {
		t.Errorf("findFeedLink = %q", got)
	}
}

func TestUserName(t *testing.T) {
	for in, want := range map[string]string{
		"https://miki.one/":             "miki.one",
		"https://amhost.example/mikio/": "amhost.example_mikio",
		"http://127.0.0.1:8100/":        "127.0.0.1_8100",
	} {
		if got := userName(in); got != want {
			t.Errorf("userName(%q) = %q, want %q", in, got, want)
		}
	}
}
