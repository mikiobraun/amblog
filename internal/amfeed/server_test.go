package amfeed

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"atomicmicro.blog/internal/atom"
	"atomicmicro.blog/internal/sign"
)

type testEnv struct {
	t     *testing.T
	srv   *Server
	url   string
	admin string
	clock time.Time
}

// tick advances the server clock by one second.
func (env *testEnv) tick() {
	env.clock = env.clock.Add(time.Second)
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	ts := httptest.NewUnstartedServer(nil)
	baseURL := "http://" + ts.Listener.Addr().String()

	dir := t.TempDir()
	configPath, admin, err := Init(dir, InitOptions{
		BaseURL: baseURL, AuthorName: "Tester", Title: "Test Feed", Listen: ":0",
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(cfg.Storage.Path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	key, err := sign.LoadPrivateKey(cfg.Signing.PrivateKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := NewServer(cfg, store, key)
	if err != nil {
		t.Fatal(err)
	}
	env := &testEnv{t: t, srv: srv, url: baseURL, admin: admin,
		clock: time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)}
	srv.now = func() time.Time { return env.clock }
	ts.Config.Handler = srv
	ts.Start()
	t.Cleanup(ts.Close)
	return env
}

func (env *testEnv) do(method, path, token, body string, header ...string) (*http.Response, string) {
	env.t.Helper()
	req, err := http.NewRequest(method, env.url+path, strings.NewReader(body))
	if err != nil {
		env.t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		env.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp, string(data)
}

func (env *testEnv) expect(resp *http.Response, body string, status int) {
	env.t.Helper()
	if resp.StatusCode != status {
		env.t.Fatalf("%s %s: status %d, want %d: %s", resp.Request.Method, resp.Request.URL, resp.StatusCode, status, body)
	}
}

func (env *testEnv) keys() *sign.KeyDoc {
	env.t.Helper()
	resp, body := env.do("GET", "/"+sign.KeysPath, "", "")
	env.expect(resp, body, 200)
	var doc sign.KeyDoc
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		env.t.Fatal(err)
	}
	return &doc
}

func (env *testEnv) feed() *atom.Feed {
	env.t.Helper()
	resp, body := env.do("GET", "/feed.atom", "", "")
	env.expect(resp, body, 200)
	f, err := atom.Parse([]byte(body))
	if err != nil {
		env.t.Fatal(err)
	}
	keys := env.keys()
	for _, e := range f.Entries {
		if err := sign.Verify(e, keys); err != nil {
			env.t.Fatalf("entry %s: %v\n%s", e.ID, err, body)
		}
	}
	return f
}

// An identity with a path (as on a shared host) gets its own key document and
// tag: ids that can't collide with other identities on the same host.
func TestPathIdentity(t *testing.T) {
	env := newTestEnv(t)
	cfg := *env.srv.cfg
	cfg.Feed.BaseURL = "https://amhost.example/mikio"
	cfg.Feed.AuthorURI = "https://amhost.example/mikio/"
	srv, err := NewServer(&cfg, env.srv.store, env.srv.key)
	if err != nil {
		t.Fatal(err)
	}
	do := func(method, path, token, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec
	}

	if rec := do("POST", "/api/entries", env.admin, `{"content":"hi"}`); rec.Code != 201 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	f, err := atom.Parse(do("GET", "/feed.atom", "", "").Body.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if id := f.Entries[0].ID; !strings.HasPrefix(id, "tag:amhost.example,") || !strings.HasSuffix(id, ":mikio/post-1") {
		t.Errorf("entry id = %q", id)
	}
	if body := do("GET", "/", "", "").Body.String(); !strings.Contains(body, `href="https://amhost.example/mikio/atomic-keys"`) {
		t.Errorf("profile doesn't link the key document: %s", body)
	}
}

func TestPostFeedDelete(t *testing.T) {
	env := newTestEnv(t)

	resp, body := env.do("POST", "/api/entries", "", `{"content":"hi"}`)
	env.expect(resp, body, 401)

	resp, body = env.do("POST", "/api/entries", env.admin, `{"content":"hello\nworld <b>&</b>"}`)
	env.expect(resp, body, 201)
	if loc := resp.Header.Get("Location"); loc != env.url+"/posts/1" {
		t.Errorf("Location = %q", loc)
	}

	f := env.feed()
	if len(f.Entries) != 1 || f.Entries[0].Content.Body != "hello\nworld <b>&</b>" {
		t.Fatalf("unexpected feed entries: %+v", f.Entries)
	}
	first := f.Entries[0]
	// The port goes into the path part, so feeds on one host don't collide.
	port := env.url[strings.LastIndex(env.url, ":")+1:]
	if !strings.HasPrefix(first.ID, "tag:127.0.0.1,") || !strings.HasSuffix(first.ID, ":"+port+"/post-1") {
		t.Errorf("entry id = %q", first.ID)
	}

	// Conditional GET.
	resp, _ = env.do("GET", "/feed.atom", "", "")
	resp, body = env.do("GET", "/feed.atom", "", "", "If-None-Match", resp.Header.Get("ETag"))
	env.expect(resp, body, 304)

	// Reply, fetched as a single Atom entry.
	resp, body = env.do("POST", "/api/entries", env.admin,
		`{"content":"a reply","in_reply_to":{"ref":"`+first.ID+`","href":"`+env.url+`/posts/1"}}`)
	env.expect(resp, body, 201)
	resp, body = env.do("GET", "/posts/2", "", "", "Accept", atom.MediaType)
	env.expect(resp, body, 200)
	doc, err := atom.Parse([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	reply := doc.Entries[0]
	if reply.InReplyTo == nil || reply.InReplyTo.Ref != first.ID {
		t.Fatalf("reply in-reply-to = %+v", reply.InReplyTo)
	}
	if err := sign.Verify(reply, env.keys()); err != nil {
		t.Fatal(err)
	}

	// Delete leaves a signed tombstone, which moves to the top of the feed.
	env.tick()
	resp, body = env.do("DELETE", "/api/entries/1", env.admin, "")
	env.expect(resp, body, 204)
	f = env.feed()
	if len(f.Entries) != 2 || f.Entries[0].ID != first.ID || f.Entries[0].Content.Body != "" {
		t.Fatalf("tombstone not as expected: %+v", f.Entries[0])
	}
	resp, body = env.do("GET", "/posts/1", "", "")
	env.expect(resp, body, 410)
}

// feedIDs walks the feed's next links from the first page and returns all
// entry ids in order.
func (env *testEnv) feedIDs() []string {
	env.t.Helper()
	var ids []string
	path := "/feed.atom"
	for pages := 0; path != ""; pages++ {
		if pages > 100 {
			env.t.Fatal("too many pages, next links loop?")
		}
		resp, body := env.do("GET", path, "", "")
		env.expect(resp, body, 200)
		f, err := atom.Parse([]byte(body))
		if err != nil {
			env.t.Fatal(err)
		}
		for _, e := range f.Entries {
			ids = append(ids, e.ID[strings.LastIndex(e.ID, "-")+1:])
		}
		path = ""
		for _, l := range f.Links {
			if l.Rel == "next" {
				path = strings.TrimPrefix(l.Href, env.url)
			}
		}
	}
	return ids
}

func TestFeedPaging(t *testing.T) {
	env := newTestEnv(t)
	env.srv.feedPageSize = 2

	// Posts 1-3 share a timestamp, so their order relies on the seq tiebreak.
	for i := range 5 {
		if i >= 3 {
			env.tick()
		}
		resp, body := env.do("POST", "/api/entries", env.admin, `{"content":"post"}`)
		env.expect(resp, body, 201)
	}
	if got, want := strings.Join(env.feedIDs(), " "), "5 4 3 2 1"; got != want {
		t.Errorf("feed order = %s, want %s", got, want)
	}

	env.tick()
	resp, body := env.do("DELETE", "/api/entries/2", env.admin, "")
	env.expect(resp, body, 204)
	if got, want := strings.Join(env.feedIDs(), " "), "2 5 4 3 1"; got != want {
		t.Errorf("feed order after delete = %s, want %s", got, want)
	}

	resp, body = env.do("GET", "/feed.atom?before=yesterday,3", "", "")
	env.expect(resp, body, 400)

	// The API pages the same way.
	resp, body = env.do("GET", "/api/entries?limit=3", env.admin, "")
	env.expect(resp, body, 200)
	var page struct {
		Entries []struct{ Seq int64 }
		Next    string
	}
	if err := json.Unmarshal([]byte(body), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 3 || page.Entries[0].Seq != 2 || page.Next == "" {
		t.Fatalf("api page 1: %s", body)
	}
	resp, body = env.do("GET", "/api/entries?limit=3&before="+page.Next, env.admin, "")
	env.expect(resp, body, 200)
	page.Next = ""
	if err := json.Unmarshal([]byte(body), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 2 || page.Entries[1].Seq != 1 || page.Next != "" {
		t.Fatalf("api page 2: %s", body)
	}
}

func TestTokens(t *testing.T) {
	env := newTestEnv(t)

	resp, body := env.do("POST", "/api/tokens", env.admin, `{"name":"phone"}`)
	env.expect(resp, body, 201)
	var created struct{ Token string }
	if err := json.Unmarshal([]byte(body), &created); err != nil {
		t.Fatal(err)
	}

	resp, body = env.do("POST", "/api/entries", created.Token, `{"content":"from phone"}`)
	env.expect(resp, body, 201)

	resp, body = env.do("POST", "/api/tokens", created.Token, `{"name":"another"}`)
	env.expect(resp, body, 403)

	resp, body = env.do("POST", "/api/tokens", env.admin, `{"name":"old","expires":"2020-01-01T00:00:00Z"}`)
	env.expect(resp, body, 201)
	json.Unmarshal([]byte(body), &created)
	resp, body = env.do("POST", "/api/entries", created.Token, `{"content":"expired"}`)
	env.expect(resp, body, 401)
}

func TestChallenges(t *testing.T) {
	env := newTestEnv(t)
	const value = "abc_DEF-123.https://agg.example"

	resp, body := env.do("PUT", "/api/challenges/abc_DEF-123", "", value)
	env.expect(resp, body, 401)
	resp, body = env.do("PUT", "/api/challenges/abc_DEF-123", env.admin, value)
	env.expect(resp, body, 204)

	resp, body = env.do("GET", "/atomic-challenge/abc_DEF-123", "", "")
	env.expect(resp, body, 200)
	if body != value {
		t.Errorf("challenge = %q, want %q", body, value)
	}
	resp, body = env.do("GET", "/atomic-challenge/unknown", "", "")
	env.expect(resp, body, 404)

	for _, bad := range []struct{ token, value string }{
		{"has.dot", value},
		{"ok", ""},
		{"ok", "line\nbreak"},
		{"ok", strings.Repeat("x", maxChallengeValue+1)},
	} {
		resp, body = env.do("PUT", "/api/challenges/"+bad.token, env.admin, bad.value)
		if resp.StatusCode != 400 {
			t.Errorf("PUT %q %q: status %d, want 400: %s", bad.token, bad.value, resp.StatusCode, body)
		}
	}

	env.clock = env.clock.Add(challengeTTL)
	resp, body = env.do("GET", "/atomic-challenge/abc_DEF-123", "", "")
	env.expect(resp, body, 404)
}

func TestCreateEntryValidation(t *testing.T) {
	env := newTestEnv(t)
	for _, body := range []string{
		`{"content":""}`,
		`{"content":"x","content_type":"xhtml"}`,
		`{"content":"x","in_reply_to":{"href":"https://x.example/1"}}`,
		`{"content":"x","unknown":1}`,
	} {
		resp, respBody := env.do("POST", "/api/entries", env.admin, body)
		if resp.StatusCode != 400 {
			t.Errorf("%s: status %d, want 400: %s", body, resp.StatusCode, respBody)
		}
	}
}
