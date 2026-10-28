package island

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// Databases from before backfill get the new column; their feeds have no
// history to read.
func TestStoreMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE feeds (
		id INTEGER PRIMARY KEY, url TEXT NOT NULL UNIQUE, atom_id TEXT NOT NULL DEFAULT '', title TEXT NOT NULL DEFAULT '',
		html_url TEXT NOT NULL DEFAULT '', etag TEXT NOT NULL DEFAULT '', last_modified TEXT NOT NULL DEFAULT '',
		last_seen TEXT NOT NULL DEFAULT '', interval_s INTEGER NOT NULL, next_poll TEXT NOT NULL,
		last_error TEXT NOT NULL DEFAULT '', failures INTEGER NOT NULL DEFAULT 0);
		INSERT INTO feeds (id, url, interval_s, next_poll) VALUES (1, 'https://example.com/feed.atom', 60, '')`)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(path, Schema{WantedFeeds: `SELECT id FROM feeds`, MemberFeeds: `SELECT id FROM feeds`})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	f, err := store.GetFeed(1)
	if err != nil || f.URL != "https://example.com/feed.atom" || f.BackfillNext != "" {
		t.Fatalf("old feed: %+v, %v", f, err)
	}
	f.BackfillNext = "https://example.com/feed.atom?page=2"
	if err := store.SaveFeed(f); err != nil {
		t.Fatal(err)
	}
}

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
