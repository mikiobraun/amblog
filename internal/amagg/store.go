package amagg

import (
	"database/sql"
	"errors"
	"strings"
	"time"

	"atomicmicro.blog/internal/paging"
	_ "modernc.org/sqlite"
)

var (
	ErrNotFound      = errors.New("not found")
	ErrInvalidInvite = errors.New("invalid or used invite code")
)

var schema = []string{
	`PRAGMA journal_mode=WAL`,
	`PRAGMA busy_timeout=5000`,
	`PRAGMA foreign_keys=ON`,
	// Feeds the aggregator polls: members' own feeds and followed feeds.
	`CREATE TABLE IF NOT EXISTS feeds (
		id            INTEGER PRIMARY KEY,
		url           TEXT NOT NULL UNIQUE,
		atom_id       TEXT NOT NULL DEFAULT '',
		title         TEXT NOT NULL DEFAULT '',
		html_url      TEXT NOT NULL DEFAULT '',
		etag          TEXT NOT NULL DEFAULT '',
		last_modified TEXT NOT NULL DEFAULT '',
		last_seen     TEXT NOT NULL DEFAULT '', -- newest entry updated seen, RFC 3339 UTC
		interval_s    INTEGER NOT NULL,
		next_poll     TEXT NOT NULL,
		last_error    TEXT NOT NULL DEFAULT '',
		failures      INTEGER NOT NULL DEFAULT 0
	)`,
	`CREATE TABLE IF NOT EXISTS users (
		id         INTEGER PRIMARY KEY,
		name       TEXT NOT NULL UNIQUE,
		author_uri TEXT NOT NULL UNIQUE,
		feed_id    INTEGER NOT NULL REFERENCES feeds (id),
		joined     TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS tokens (
		hash    TEXT PRIMARY KEY,
		user_id INTEGER NOT NULL REFERENCES users (id),
		created TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS invites (
		hash    TEXT PRIMARY KEY,
		created TEXT NOT NULL,
		used_by INTEGER REFERENCES users (id)
	)`,
	`CREATE TABLE IF NOT EXISTS follows (
		user_id INTEGER NOT NULL REFERENCES users (id),
		feed_id INTEGER NOT NULL REFERENCES feeds (id),
		created TEXT NOT NULL,
		PRIMARY KEY (user_id, feed_id)
	)`,
	// Ingested entries. n is the local post number (/posts/{n}); id the Atom
	// entry id. feed_id is the feed the entry arrived through, NULL for entries
	// that came another way (e.g. a fetched parent); only entries with a feed
	// appear in timelines. xml is the entry as received (author filled in if
	// it had none).
	`CREATE TABLE IF NOT EXISTS entries (
		n               INTEGER PRIMARY KEY,
		id              TEXT NOT NULL UNIQUE,
		author_uri      TEXT NOT NULL,
		fetched_from    TEXT NOT NULL,
		feed_id         INTEGER REFERENCES feeds (id),
		in_reply_to_ref TEXT NOT NULL,
		sort_time       TEXT NOT NULL, -- published (or updated), RFC 3339 UTC
		updated         TEXT NOT NULL, -- RFC 3339 UTC
		verified        INTEGER NOT NULL,
		xml             TEXT NOT NULL,
		received        TEXT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS entries_timeline ON entries (feed_id, sort_time, n)`,
	`CREATE INDEX IF NOT EXISTS entries_thread ON entries (in_reply_to_ref)`,
}

type Store struct {
	db *sql.DB
}

func OpenStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// One connection: SQLite has a single writer anyway, and this avoids
	// SQLITE_BUSY between our own connections.
	db.SetMaxOpenConns(1)
	for _, stmt := range schema {
		if _, err := db.Exec(stmt); err != nil {
			db.Close()
			return nil, err
		}
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func nowString() string { return paging.FormatTime(time.Now()) }

// --- feeds ---

type Feed struct {
	ID           int64
	URL          string
	AtomID       string
	Title        string
	HTMLURL      string
	ETag         string
	LastModified string
	LastSeen     string
	Interval     time.Duration
	NextPoll     string
	LastError    string
	Failures     int
}

const feedColumns = `id, url, atom_id, title, html_url, etag, last_modified, last_seen, interval_s, next_poll, last_error, failures`

func scanFeed(row interface{ Scan(...any) error }) (*Feed, error) {
	var f Feed
	var interval int64
	err := row.Scan(&f.ID, &f.URL, &f.AtomID, &f.Title, &f.HTMLURL, &f.ETag, &f.LastModified, &f.LastSeen,
		&interval, &f.NextPoll, &f.LastError, &f.Failures)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	f.Interval = time.Duration(interval) * time.Second
	return &f, err
}

// AddFeed returns the feed with the given URL, creating it if needed.
func (s *Store) AddFeed(url string, interval time.Duration, nextPoll string) (*Feed, error) {
	_, err := s.db.Exec(`INSERT INTO feeds (url, interval_s, next_poll) VALUES (?, ?, ?) ON CONFLICT (url) DO NOTHING`,
		url, int64(interval/time.Second), nextPoll)
	if err != nil {
		return nil, err
	}
	return scanFeed(s.db.QueryRow(`SELECT `+feedColumns+` FROM feeds WHERE url = ?`, url))
}

func (s *Store) GetFeed(id int64) (*Feed, error) {
	return scanFeed(s.db.QueryRow(`SELECT `+feedColumns+` FROM feeds WHERE id = ?`, id))
}

// SaveFeed stores the feed's metadata and polling state.
func (s *Store) SaveFeed(f *Feed) error {
	_, err := s.db.Exec(`UPDATE feeds SET atom_id = ?, title = ?, html_url = ?, etag = ?, last_modified = ?,
		last_seen = ?, interval_s = ?, next_poll = ?, last_error = ?, failures = ? WHERE id = ?`,
		f.AtomID, f.Title, f.HTMLURL, f.ETag, f.LastModified, f.LastSeen,
		int64(f.Interval/time.Second), f.NextPoll, f.LastError, f.Failures, f.ID)
	return err
}

// DueFeeds returns feeds that someone follows or that belong to a member,
// whose next poll is due at time t.
func (s *Store) DueFeeds(t string, limit int) ([]*Feed, error) {
	rows, err := s.db.Query(`SELECT `+feedColumns+` FROM feeds
		WHERE next_poll <= ? AND (id IN (SELECT feed_id FROM users) OR id IN (SELECT feed_id FROM follows))
		ORDER BY next_poll LIMIT ?`, t, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var feeds []*Feed
	for rows.Next() {
		f, err := scanFeed(rows)
		if err != nil {
			return nil, err
		}
		feeds = append(feeds, f)
	}
	return feeds, rows.Err()
}

// --- users, tokens, invites ---

type User struct {
	ID        int64
	Name      string
	AuthorURI string
	FeedID    int64
	Joined    string
}

const userColumns = `id, name, author_uri, feed_id, joined`

func scanUser(row interface{ Scan(...any) error }) (*User, error) {
	var u User
	err := row.Scan(&u.ID, &u.Name, &u.AuthorURI, &u.FeedID, &u.Joined)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &u, err
}

func (s *Store) UserByAuthor(authorURI string) (*User, error) {
	return scanUser(s.db.QueryRow(`SELECT `+userColumns+` FROM users WHERE author_uri = ?`, authorURI))
}

func (s *Store) UserByName(name string) (*User, error) {
	return scanUser(s.db.QueryRow(`SELECT `+userColumns+` FROM users WHERE name = ?`, name))
}

// CreateUser creates a member. If inviteHash is not empty, the invite is
// consumed in the same transaction; an unknown or used invite fails with
// ErrInvalidInvite.
func (s *Store) CreateUser(name, authorURI string, feedID int64, inviteHash string) (*User, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT INTO users (name, author_uri, feed_id, joined) VALUES (?, ?, ?, ?)`,
		name, authorURI, feedID, nowString())
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	if inviteHash != "" {
		res, err := tx.Exec(`UPDATE invites SET used_by = ? WHERE hash = ? AND used_by IS NULL`, id, inviteHash)
		if err != nil {
			return nil, err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil, ErrInvalidInvite
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return scanUser(s.db.QueryRow(`SELECT `+userColumns+` FROM users WHERE id = ?`, id))
}

func (s *Store) CreateInvite(hash string) error {
	_, err := s.db.Exec(`INSERT INTO invites (hash, created) VALUES (?, ?)`, hash, nowString())
	return err
}

// InviteUsable reports whether the invite exists and is unused.
func (s *Store) InviteUsable(hash string) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM invites WHERE hash = ? AND used_by IS NULL`, hash).Scan(&n)
	return n == 1, err
}

func (s *Store) CreateToken(hash string, userID int64) error {
	_, err := s.db.Exec(`INSERT INTO tokens (hash, user_id, created) VALUES (?, ?, ?)`, hash, userID, nowString())
	return err
}

// UserByToken returns the user a token hash belongs to, or ErrNotFound.
func (s *Store) UserByToken(hash string) (*User, error) {
	return scanUser(s.db.QueryRow(`SELECT `+prefixed("u.", userColumns)+` FROM users u
		JOIN tokens t ON t.user_id = u.id WHERE t.hash = ?`, hash))
}

// --- follows ---

func (s *Store) Follow(userID, feedID int64) error {
	_, err := s.db.Exec(`INSERT INTO follows (user_id, feed_id, created) VALUES (?, ?, ?)
		ON CONFLICT DO NOTHING`, userID, feedID, nowString())
	return err
}

// Unfollow removes a follow by feed URL; it returns ErrNotFound if the user
// doesn't follow that feed.
func (s *Store) Unfollow(userID int64, feedURL string) error {
	res, err := s.db.Exec(`DELETE FROM follows WHERE user_id = ? AND feed_id = (SELECT id FROM feeds WHERE url = ?)`,
		userID, feedURL)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) Following(userID int64) ([]*Feed, error) {
	rows, err := s.db.Query(`SELECT `+prefixed("f.", feedColumns)+` FROM feeds f
		JOIN follows fo ON fo.feed_id = f.id WHERE fo.user_id = ? ORDER BY fo.created`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var feeds []*Feed
	for rows.Next() {
		f, err := scanFeed(rows)
		if err != nil {
			return nil, err
		}
		feeds = append(feeds, f)
	}
	return feeds, rows.Err()
}

// --- entries ---

type Entry struct {
	N            int64
	ID           string
	AuthorURI    string
	FetchedFrom  string
	FeedID       sql.NullInt64
	InReplyToRef string
	SortTime     string
	Updated      string
	Verified     bool
	XML          string
	Received     string
}

const entryColumns = `n, id, author_uri, fetched_from, feed_id, in_reply_to_ref, sort_time, updated, verified, xml, received`

func scanEntry(row interface{ Scan(...any) error }) (*Entry, error) {
	var e Entry
	err := row.Scan(&e.N, &e.ID, &e.AuthorURI, &e.FetchedFrom, &e.FeedID, &e.InReplyToRef,
		&e.SortTime, &e.Updated, &e.Verified, &e.XML, &e.Received)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &e, err
}

func (s *Store) EntryByID(id string) (*Entry, error) {
	return scanEntry(s.db.QueryRow(`SELECT `+entryColumns+` FROM entries WHERE id = ?`, id))
}

func (s *Store) EntryByN(n int64) (*Entry, error) {
	return scanEntry(s.db.QueryRow(`SELECT `+entryColumns+` FROM entries WHERE n = ?`, n))
}

func (s *Store) InsertEntry(e *Entry) error {
	res, err := s.db.Exec(`INSERT INTO entries (id, author_uri, fetched_from, feed_id, in_reply_to_ref,
		sort_time, updated, verified, xml, received) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ID, e.AuthorURI, e.FetchedFrom, e.FeedID, e.InReplyToRef, e.SortTime, e.Updated, e.Verified, e.XML, e.Received)
	if err != nil {
		return err
	}
	e.N, err = res.LastInsertId()
	return err
}

// ReplaceEntry overwrites a stored entry (by n) with a newer version.
func (s *Store) ReplaceEntry(e *Entry) error {
	_, err := s.db.Exec(`UPDATE entries SET author_uri = ?, fetched_from = ?, feed_id = ?, in_reply_to_ref = ?,
		sort_time = ?, updated = ?, verified = ?, xml = ?, received = ? WHERE n = ?`,
		e.AuthorURI, e.FetchedFrom, e.FeedID, e.InReplyToRef, e.SortTime, e.Updated, e.Verified, e.XML, e.Received, e.N)
	return err
}

// Timeline returns up to limit entries, newest first by sort_time, from the
// island (all members' feeds) plus, if userID != 0, that user's follows.
// next is the cursor for the following page, or nil.
func (s *Store) Timeline(userID int64, before *paging.Cursor, limit int) (entries []*Entry, next *paging.Cursor, err error) {
	query := `SELECT ` + entryColumns + ` FROM entries WHERE feed_id IN (SELECT feed_id FROM users`
	args := []any{}
	if userID != 0 {
		query += ` UNION SELECT feed_id FROM follows WHERE user_id = ?`
		args = append(args, userID)
	}
	query += `)`
	if before != nil {
		query += ` AND (sort_time, n) < (?, ?)`
		args = append(args, before.Time, before.Seq)
	}
	query += ` ORDER BY sort_time DESC, n DESC LIMIT ?`
	args = append(args, limit+1) // one extra to know whether there is a next page

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, nil, err
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	if len(entries) > limit {
		entries = entries[:limit]
		last := entries[limit-1]
		next = &paging.Cursor{Time: last.SortTime, Seq: last.N}
	}
	return entries, next, nil
}

// prefixed qualifies a comma-separated column list with a table alias.
func prefixed(alias, columns string) string {
	cols := strings.Split(columns, ",")
	for i, c := range cols {
		cols[i] = alias + strings.TrimSpace(c)
	}
	return strings.Join(cols, ", ")
}
