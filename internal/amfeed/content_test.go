package amfeed

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"atomicmicro.blog/internal/atom"
)

// withContent gives the test server a content folder and returns its path.
func (env *testEnv) withContent() string {
	dir := env.t.TempDir()
	env.srv.cfg.Content = &ContentConfig{Dir: dir, Pattern: "*.md"}
	return dir
}

func (env *testEnv) sync() {
	env.t.Helper()
	if err := env.srv.SyncContent(); err != nil {
		env.t.Fatal(err)
	}
	env.srv.Wait()
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func entriesByTitle(f *atom.Feed) map[string]*atom.Entry {
	m := map[string]*atom.Entry{}
	for _, e := range f.Entries {
		m[e.Title] = e
	}
	return m
}

func TestContentFolder(t *testing.T) {
	env := newTestEnv(t)
	dir := env.withContent()
	writeFile(t, dir, "2026-10-26.md", "---\ndate: 2026-10-26\nsources_read:\n  - a-source\n---\n\n## Headlines\n\n- Something *happened*\n")
	writeFile(t, dir, "notes.md", "# My Notes\n\nSee [[2026-10-26|yesterday]] and [[private-note]].\n")
	writeFile(t, dir, "ignored.txt", "not Markdown")
	env.sync()

	f := env.feed() // verifies every signature
	if len(f.Entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(f.Entries))
	}
	byTitle := entriesByTitle(f)
	brief, notes := byTitle["2026-10-26"], byTitle["My Notes"]
	if brief == nil || notes == nil {
		t.Fatalf("titles: %v", byTitle)
	}
	if brief.Published != "2026-10-26T00:00:00Z" {
		t.Errorf("published from frontmatter date: %s", brief.Published)
	}
	if want := env.srv.idPrefix + "2026-10:" + env.srv.idPath + "content/2026-10-26"; brief.ID != want {
		t.Errorf("id %s, want %s", brief.ID, want)
	}
	if brief.Content.Type != "html" || !strings.Contains(brief.Content.Body, "<h2>Headlines</h2>") || !strings.Contains(brief.Content.Body, "<em>happened</em>") {
		t.Errorf("brief content: %+v", brief.Content)
	}
	// The heading became the title and left the body; the wikilink to a
	// published file (new in this same sync) is a link, the other plain text.
	if strings.Contains(notes.Content.Body, "My Notes") {
		t.Errorf("title heading still in the body: %s", notes.Content.Body)
	}
	if link := `<a href="` + brief.LinkHref("alternate") + `">yesterday</a>`; !strings.Contains(notes.Content.Body, link) {
		t.Errorf("want %s in %s", link, notes.Content.Body)
	}
	if !strings.Contains(notes.Content.Body, "and private-note.") || strings.Contains(notes.Content.Body, "[[") {
		t.Errorf("unpublished wikilink: %s", notes.Content.Body)
	}

	// Nothing changed: nothing is touched.
	env.tick()
	env.sync()
	if again := entriesByTitle(env.feed()); again["My Notes"].Updated != notes.Updated {
		t.Errorf("unchanged file got a new updated: %s", again["My Notes"].Updated)
	}

	// An edit updates the post in place.
	env.tick()
	writeFile(t, dir, "notes.md", "# My Notes\n\nRewritten.\n")
	env.sync()
	edited := entriesByTitle(env.feed())["My Notes"]
	if edited.ID != notes.ID || edited.LinkHref("alternate") != notes.LinkHref("alternate") || edited.Updated == notes.Updated ||
		!strings.Contains(edited.Content.Body, "Rewritten.") {
		t.Errorf("edit: %+v", edited)
	}

	// The API doesn't delete file posts.
	seq := strings.TrimPrefix(brief.LinkHref("alternate"), env.url+"/posts/")
	resp, body := env.do("DELETE", "/api/entries/"+seq, env.admin, "")
	env.expect(resp, body, 409)

	// Removing the file turns the post into a tombstone; a file that comes
	// back revives it.
	env.tick()
	os.Remove(filepath.Join(dir, "2026-10-26.md"))
	env.sync()
	var tomb *atom.Entry
	for _, e := range env.feed().Entries {
		if e.ID == brief.ID {
			tomb = e
		}
	}
	if tomb == nil || tomb.Title != "" || tomb.Content.Body != "" {
		t.Fatalf("tombstone: %+v", tomb)
	}
	env.tick()
	writeFile(t, dir, "2026-10-26.md", "back\n")
	env.sync()
	if back := entriesByTitle(env.feed())["2026-10-26"]; back == nil || back.ID != brief.ID || back.Published != brief.Published {
		t.Errorf("revived: %+v", back)
	}
}

func TestContentFrontmatter(t *testing.T) {
	env := newTestEnv(t)
	dir := env.withContent()
	writeFile(t, dir, "a.md", "---\ntitle: \"Securing \\\"It\\\"\"\nid: tag:example.com,2026:custom\ndate: 2026-10-27T08:30:00+02:00\nlinks:\n  - https://example.com/\n---\nBody\n")
	writeFile(t, dir, "bad-date.md", "---\ndate: yesterday\n---\nBody\n")
	writeFile(t, dir, "unclosed.md", "---\ntitle: x\nBody\n")
	writeFile(t, dir, ".hidden.md", "Hidden\n")
	env.sync()

	f := env.feed()
	if len(f.Entries) != 1 {
		t.Fatalf("got %d entries, want only a.md: %+v", len(f.Entries), entriesByTitle(f))
	}
	e := f.Entries[0]
	if e.Title != `Securing "It"` || e.ID != "tag:example.com,2026:custom" || e.Published != "2026-10-27T06:30:00Z" {
		t.Errorf("entry: title %q id %q published %q", e.Title, e.ID, e.Published)
	}
}

// A missing folder is an error, not a reason to delete every post.
func TestContentFolderMissing(t *testing.T) {
	env := newTestEnv(t)
	dir := env.withContent()
	writeFile(t, dir, "a.md", "A\n")
	env.sync()
	os.RemoveAll(dir)
	if err := env.srv.SyncContent(); err == nil {
		t.Fatal("sync of a missing folder succeeded")
	}
	if f := env.feed(); len(f.Entries) != 1 || f.Entries[0].Content.Body == "" {
		t.Errorf("post was touched: %+v", f.Entries)
	}
}

// Databases from before the content folder get the new columns.
func TestStoreMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE entries (
		seq INTEGER PRIMARY KEY, id TEXT NOT NULL UNIQUE, published TEXT NOT NULL, updated TEXT NOT NULL,
		title TEXT NOT NULL, content_type TEXT NOT NULL, content TEXT NOT NULL,
		in_reply_to_ref TEXT NOT NULL, in_reply_to_href TEXT NOT NULL, verb TEXT NOT NULL,
		key_id TEXT NOT NULL, signature TEXT NOT NULL, deleted INTEGER NOT NULL);
		INSERT INTO entries VALUES (1, 'tag:x,2026-10:post-1', 'p', 'u', '', 'text', 'hi', '', '', '', 'k', 's', 0)`)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	e, err := store.GetEntry(1)
	if err != nil || e.Content != "hi" || e.SourcePath != "" {
		t.Fatalf("old entry: %+v, %v", e, err)
	}
	if m, err := store.SourceEntries(); err != nil || len(m) != 0 {
		t.Fatalf("source entries: %v, %v", m, err)
	}
}
