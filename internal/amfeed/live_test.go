package amfeed

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"atomicmicro.blog/internal/atom"
	"atomicmicro.blog/internal/websub"
)

// subscriber is a WebSub subscriber that confirms every request and records
// what is pushed to it.
type subscriber struct {
	mu     sync.Mutex
	pushes []string // bodies with a valid signature
	bad    int      // pushes with a bad signature
	secret string
}

func (sub *subscriber) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		io.WriteString(w, r.URL.Query().Get("hub.challenge"))
		return
	}
	body, _ := io.ReadAll(r.Body)
	sub.mu.Lock()
	defer sub.mu.Unlock()
	if websub.CheckSignature(sub.secret, r.Header.Get("X-Hub-Signature"), body) {
		sub.pushes = append(sub.pushes, string(body))
	} else {
		sub.bad++
	}
	w.WriteHeader(http.StatusNoContent)
}

func TestWebSubHub(t *testing.T) {
	env := newTestEnv(t)
	sub := &subscriber{secret: "s3cret"}
	ts := httptest.NewServer(sub)
	defer ts.Close()

	// The feed names its hub.
	if hub := linkHref(env.feed().Links, "hub"); hub != env.url+websub.HubPath {
		t.Fatalf("hub link = %q", hub)
	}

	form := func(topic string) string {
		return url.Values{"hub.mode": {"subscribe"}, "hub.topic": {topic}, "hub.callback": {ts.URL + "/cb"},
			"hub.secret": {sub.secret}}.Encode()
	}
	ct := []string{"Content-Type", "application/x-www-form-urlencoded"}
	resp, body := env.do("POST", websub.HubPath, "", form(env.url+"/other.atom"), ct...)
	env.expect(resp, body, http.StatusBadRequest) // only the feed is a topic
	resp, body = env.do("POST", websub.HubPath, "", form(env.url+"/feed.atom"), ct...)
	env.expect(resp, body, http.StatusAccepted)
	env.srv.Wait()
	if subs, _ := env.srv.store.Subscriptions(env.clock.Format("2006-01-02T15:04:05Z")); len(subs) != 1 {
		t.Fatalf("subscriptions = %v", subs)
	}

	// A new post pushes the feed, signed with the subscriber's secret.
	env.do("POST", "/api/entries", env.admin, `{"content":"pushed"}`)
	env.srv.Wait()
	sub.mu.Lock()
	defer sub.mu.Unlock()
	if len(sub.pushes) != 1 || !strings.Contains(sub.pushes[0], "pushed") || sub.bad != 0 {
		t.Errorf("pushes = %d (bad %d)", len(sub.pushes), sub.bad)
	}
}

func TestReceiveWebMention(t *testing.T) {
	env := newTestEnv(t)
	resp, _ := env.do("POST", "/api/entries", env.admin, `{"content":"hello"}`)
	post := resp.Header.Get("Location")

	// A plain blog page linking to the post.
	var mu sync.Mutex
	status := http.StatusOK
	blog := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(status)
		io.WriteString(w, `<html><head><title>Re: hello</title></head><body><a href="`+post+`">nice</a></body></html>`)
	}))
	defer blog.Close()

	ct := []string{"Content-Type", "application/x-www-form-urlencoded"}
	mention := url.Values{"source": {blog.URL + "/reply"}, "target": {post}}.Encode()
	resp, body := env.do("POST", "/webmention", "", mention, ct...)
	env.expect(resp, body, http.StatusAccepted)
	env.srv.Wait()

	replies := func() *atom.Feed {
		t.Helper()
		resp, body := env.do("GET", strings.TrimPrefix(post, env.url)+"/replies.atom", "", "")
		env.expect(resp, body, http.StatusOK)
		f, err := atom.Parse([]byte(body))
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	f := replies()
	if len(f.Entries) != 1 || f.Entries[0].ID != blog.URL+"/reply" || f.Entries[0].Title != "Re: hello" {
		t.Fatalf("replies = %+v", f.Entries)
	}

	// The page is gone: the mention is removed.
	mu.Lock()
	status = http.StatusGone
	mu.Unlock()
	env.do("POST", "/webmention", "", mention, ct...)
	env.srv.Wait()
	if f := replies(); len(f.Entries) != 0 {
		t.Errorf("replies after 410 = %d", len(f.Entries))
	}

	// Mentions of things that aren't our posts are rejected.
	bad := url.Values{"source": {blog.URL + "/reply"}, "target": {env.url + "/feed.atom"}}.Encode()
	resp, body = env.do("POST", "/webmention", "", bad, ct...)
	env.expect(resp, body, http.StatusBadRequest)
}

func linkHref(links []atom.Link, rel string) string {
	for _, l := range links {
		if l.Rel == rel {
			return l.Href
		}
	}
	return ""
}
