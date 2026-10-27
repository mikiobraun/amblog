package island

import "testing"

// The island is tested end to end through amagg (internal/amagg) and ampress
// (internal/ampress); these are its pure functions.

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

func TestNofollow(t *testing.T) {
	if got := Nofollow(`<p><a href="x">x</a></p>`); got != `<p><a rel="nofollow ugc" href="x">x</a></p>` {
		t.Errorf("Nofollow = %s", got)
	}
}
