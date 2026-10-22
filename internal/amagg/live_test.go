package amagg

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"atomicmicro.blog/internal/atom"
)

// TestLive covers what makes the island live without polling: WebSub pushes
// from members' feed servers, replies arriving by WebMention, and replies
// forwarded to their source.
func TestLive(t *testing.T) {
	agg := startAgg(t, nil)
	alice, bob := startFeed(t, "Alice"), startFeed(t, "Bob")
	agg.login(alice, agg.invite())
	agg.login(bob, agg.invite())
	alice.wait() // the hubs verify the subscriptions
	bob.wait()

	for _, f := range []*feedEnv{alice, bob} {
		u, err := agg.srv.store.UserByAuthor(f.url + "/")
		if err != nil {
			t.Fatal(err)
		}
		if !agg.srv.pushing(u.FeedID) {
			t.Fatalf("no active subscription for %s", f.url)
		}
	}

	// A post reaches the island by push, without a poll.
	alicePost := alice.post(t, `{"content":"pushed"}`)
	alice.wait()
	island := agg.feed("/timeline.atom", "")
	var aggPost string
	for _, e := range island.Entries {
		if e.Content.Body == "pushed" {
			aggPost = e.LinkHref("alternate")
		}
	}
	if aggPost == "" {
		t.Fatalf("pushed post not in the island: %v", contents(island))
	}

	// Bob replies to the island's copy. His feed server pushes the reply and
	// sends the island a WebMention; the island forwards the reply to Alice.
	reply(t, bob, aggPost, "bob replies")
	bob.wait()
	agg.srv.wait()
	alice.wait()
	thread := agg.feed(strings.TrimPrefix(aggPost, agg.url)+"/thread.atom", "")
	if got := strings.Join(contents(thread), " / "); got != "pushed / bob replies" {
		t.Errorf("thread = %s", got)
	}
	replies := fetchAtom(t, alicePost+"/replies.atom")
	if len(replies.Entries) != 1 || replies.Entries[0].Content.Body != "bob replies" {
		t.Errorf("alice's replies.atom = %v", contents(replies))
	}

	// A plain blog replies by WebMention: an unverified entry from that page.
	blog := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, `<html><title>A blog reply</title><a href="`+aggPost+`">this</a></html>`)
	}))
	defer blog.Close()
	resp, body := mention(t, agg.url, blog.URL+"/post", aggPost)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("mention: %d %s", resp.StatusCode, body)
	}
	agg.srv.wait()
	thread = agg.feed(strings.TrimPrefix(aggPost, agg.url)+"/thread.atom", "")
	// Replies are ordered by time, and the blog entry carries the aggregator's
	// (test) clock, so look it up by id.
	var blogReply *atom.Entry
	for _, e := range thread.Entries {
		if e.ID == blog.URL+"/post" {
			blogReply = e
		}
	}
	if len(thread.Entries) != 3 || blogReply == nil || blogReply.Title != "A blog reply" {
		t.Errorf("thread after blog mention: %d entries, blog reply %+v", len(thread.Entries), blogReply)
	}

	// Links in replies carry no ranking.
	if got := nofollow(`<p><a href="x">x</a></p>`); got != `<p><a rel="nofollow ugc" href="x">x</a></p>` {
		t.Errorf("nofollow = %s", got)
	}
}

func TestWebMentionLimits(t *testing.T) {
	agg := startAgg(t, func(c *Config) {
		c.WebMention.PerHostPerHour = 1
		c.WebMention.BlockHosts = []string{"blocked.example"}
	})
	alice := startFeed(t, "Alice")
	agg.login(alice, agg.invite())
	alice.post(t, `{"content":"hi"}`)
	alice.wait()
	target := agg.feed("/timeline.atom", "").Entries[0].LinkHref("alternate")

	resp, _ := mention(t, agg.url, "https://blocked.example/x", target)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("blocked host: %d", resp.StatusCode)
	}
	resp, _ = mention(t, agg.url, "https://blog.example/1", target)
	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("first mention: %d", resp.StatusCode)
	}
	resp, _ = mention(t, agg.url, "https://blog.example/2", target)
	if resp.StatusCode != http.StatusTooManyRequests || resp.Header.Get("Retry-After") == "" {
		t.Errorf("over the limit: %d, Retry-After %q", resp.StatusCode, resp.Header.Get("Retry-After"))
	}
	resp, _ = mention(t, agg.url, "https://blog.example/3", agg.url+"/timeline.atom")
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("target not a post: %d", resp.StatusCode)
	}
	agg.srv.wait()
}

func mention(t *testing.T, aggURL, source, target string) (*http.Response, string) {
	t.Helper()
	resp, err := http.PostForm(aggURL+"/webmention", url.Values{"source": {source}, "target": {target}})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp, string(body)
}

func fetchAtom(t *testing.T, u string) *atom.Feed {
	t.Helper()
	resp, body := request(t, "GET", u, "", "")
	if resp.StatusCode != 200 {
		t.Fatalf("GET %s: %d %s", u, resp.StatusCode, body)
	}
	f, err := atom.Parse([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	return f
}
