package amfeed

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"atomicmicro.blog/internal/atom"
	"atomicmicro.blog/internal/sign"
)

type testEnv struct {
	t     *testing.T
	url   string
	admin string
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
	ts.Config.Handler = srv
	ts.Start()
	t.Cleanup(ts.Close)
	return &testEnv{t: t, url: baseURL, admin: admin}
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
	resp, body := env.do("GET", sign.WellKnownPath, "", "")
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
	if !strings.HasPrefix(first.ID, "tag:127.0.0.1,") || !strings.HasSuffix(first.ID, ":post-1") {
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

	// Delete leaves a signed tombstone.
	resp, body = env.do("DELETE", "/api/entries/1", env.admin, "")
	env.expect(resp, body, 204)
	f = env.feed()
	if len(f.Entries) != 2 || f.Entries[1].ID != first.ID || f.Entries[1].Content.Body != "" {
		t.Fatalf("tombstone not as expected: %+v", f.Entries[1])
	}
	resp, body = env.do("GET", "/posts/1", "", "")
	env.expect(resp, body, 410)
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
