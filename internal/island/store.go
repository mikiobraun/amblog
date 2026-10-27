package island

import (
	"database/sql"
	"errors"
	"time"

	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("not found")

var schema = []string{
	`PRAGMA journal_mode=WAL`,
	`PRAGMA busy_timeout=5000`,
	`PRAGMA foreign_keys=ON`,
	// Feeds the island polls.
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
	// Ingested entries. n is the local post number; id the Atom entry id.
	// feed_id is the feed the entry arrived through, NULL for entries that
	// came another way (e.g. a fetched parent); only entries with a feed
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
	// WebSub subscriptions, one per feed. state is "pending" until the hub has
	// verified the intent, then "active" until expires.
	`CREATE TABLE IF NOT EXISTS subscriptions (
		feed_id   INTEGER PRIMARY KEY REFERENCES feeds (id),
		hub       TEXT NOT NULL,
		topic     TEXT NOT NULL,
		secret    TEXT NOT NULL,
		state     TEXT NOT NULL,
		requested TEXT NOT NULL,
		expires   TEXT NOT NULL DEFAULT ''
	)`,
}

// Schema is what an application adds to the island's database: its own
// tables, and which feeds it wants, as SQL queries selecting feed ids from
// those tables.
type Schema struct {
	Tables []string
	// WantedFeeds selects the feeds that are polled. Pushes for other feeds
	// end their subscription.
	WantedFeeds string
	// MemberFeeds selects the feeds whose replies are forwarded to their
	// sources: the island's own members.
	MemberFeeds string
}

type Store struct {
	db     *sql.DB
	schema Schema
}

func OpenStore(path string, sch Schema) (*Store, error) {
	if sch.WantedFeeds == "" || sch.MemberFeeds == "" {
		return nil, errors.New("island: schema needs WantedFeeds and MemberFeeds")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// One connection: SQLite has a single writer anyway, and this avoids
	// SQLITE_BUSY between our own connections.
	db.SetMaxOpenConns(1)
	for _, stmt := range append(schema, sch.Tables...) {
		if _, err := db.Exec(stmt); err != nil {
			db.Close()
			return nil, err
		}
	}
	return &Store{db: db, schema: sch}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// DB is the database, for the application's own tables.
func (s *Store) DB() *sql.DB { return s.db }

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

const FeedColumns = `id, url, atom_id, title, html_url, etag, last_modified, last_seen, interval_s, next_poll, last_error, failures`

func ScanFeed(row interface{ Scan(...any) error }) (*Feed, error) {
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
	return ScanFeed(s.db.QueryRow(`SELECT `+FeedColumns+` FROM feeds WHERE url = ?`, url))
}

func (s *Store) GetFeed(id int64) (*Feed, error) {
	return ScanFeed(s.db.QueryRow(`SELECT `+FeedColumns+` FROM feeds WHERE id = ?`, id))
}

// SaveFeed stores the feed's metadata and polling state.
func (s *Store) SaveFeed(f *Feed) error {
	_, err := s.db.Exec(`UPDATE feeds SET atom_id = ?, title = ?, html_url = ?, etag = ?, last_modified = ?,
		last_seen = ?, interval_s = ?, next_poll = ?, last_error = ?, failures = ? WHERE id = ?`,
		f.AtomID, f.Title, f.HTMLURL, f.ETag, f.LastModified, f.LastSeen,
		int64(f.Interval/time.Second), f.NextPoll, f.LastError, f.Failures, f.ID)
	return err
}

// DueFeeds returns wanted feeds whose next poll is due at time t.
func (s *Store) DueFeeds(t string, limit int) ([]*Feed, error) {
	rows, err := s.db.Query(`SELECT `+FeedColumns+` FROM feeds
		WHERE next_poll <= ? AND id IN (`+s.schema.WantedFeeds+`)
		ORDER BY next_poll LIMIT ?`, t, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var feeds []*Feed
	for rows.Next() {
		f, err := ScanFeed(rows)
		if err != nil {
			return nil, err
		}
		feeds = append(feeds, f)
	}
	return feeds, rows.Err()
}

// FeedWanted reports whether the feed is still polled.
func (s *Store) FeedWanted(feedID int64) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM feeds WHERE id = ? AND id IN (`+s.schema.WantedFeeds+`)`, feedID).Scan(&n)
	return n > 0, err
}

// IsMemberFeed reports whether the feed is a member's own feed.
func (s *Store) IsMemberFeed(feedID int64) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM feeds WHERE id = ? AND id IN (`+s.schema.MemberFeeds+`)`, feedID).Scan(&n)
	return n > 0, err
}

// --- WebSub subscriptions ---

type Subscription struct {
	FeedID    int64
	Hub       string
	Topic     string
	Secret    string
	State     string // "pending" or "active"
	Requested string
	Expires   string
}

func (s *Store) GetSubscription(feedID int64) (*Subscription, error) {
	var sub Subscription
	err := s.db.QueryRow(`SELECT feed_id, hub, topic, secret, state, requested, expires FROM subscriptions WHERE feed_id = ?`,
		feedID).Scan(&sub.FeedID, &sub.Hub, &sub.Topic, &sub.Secret, &sub.State, &sub.Requested, &sub.Expires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &sub, err
}

func (s *Store) PutSubscription(sub *Subscription) error {
	_, err := s.db.Exec(`INSERT INTO subscriptions (feed_id, hub, topic, secret, state, requested, expires)
		VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT (feed_id) DO UPDATE SET hub = excluded.hub, topic = excluded.topic,
		secret = excluded.secret, state = excluded.state, requested = excluded.requested, expires = excluded.expires`,
		sub.FeedID, sub.Hub, sub.Topic, sub.Secret, sub.State, sub.Requested, sub.Expires)
	return err
}

func (s *Store) DeleteSubscription(feedID int64) error {
	_, err := s.db.Exec(`DELETE FROM subscriptions WHERE feed_id = ?`, feedID)
	return err
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

const EntryColumns = `n, id, author_uri, fetched_from, feed_id, in_reply_to_ref, sort_time, updated, verified, xml, received`

func ScanEntry(row interface{ Scan(...any) error }) (*Entry, error) {
	var e Entry
	err := row.Scan(&e.N, &e.ID, &e.AuthorURI, &e.FetchedFrom, &e.FeedID, &e.InReplyToRef,
		&e.SortTime, &e.Updated, &e.Verified, &e.XML, &e.Received)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &e, err
}

// ScanEntries collects the entries of a query selecting EntryColumns.
func ScanEntries(rows *sql.Rows) ([]*Entry, error) {
	defer rows.Close()
	var entries []*Entry
	for rows.Next() {
		e, err := ScanEntry(rows)
		if err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

func (s *Store) EntryByID(id string) (*Entry, error) {
	return ScanEntry(s.db.QueryRow(`SELECT `+EntryColumns+` FROM entries WHERE id = ?`, id))
}

func (s *Store) EntryByN(n int64) (*Entry, error) {
	return ScanEntry(s.db.QueryRow(`SELECT `+EntryColumns+` FROM entries WHERE n = ?`, n))
}

// InsertEntry stores a new entry and sets e.N. If an entry with that id
// exists (another ingest path got there first), it stores nothing and
// returns inserted = false.
func (s *Store) InsertEntry(e *Entry) (inserted bool, err error) {
	res, err := s.db.Exec(`INSERT INTO entries (id, author_uri, fetched_from, feed_id, in_reply_to_ref,
		sort_time, updated, verified, xml, received) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO NOTHING`,
		e.ID, e.AuthorURI, e.FetchedFrom, e.FeedID, e.InReplyToRef, e.SortTime, e.Updated, e.Verified, e.XML, e.Received)
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return false, nil
	}
	e.N, err = res.LastInsertId()
	return err == nil, err
}

// ReplaceEntry overwrites a stored entry (by n) with a newer version.
func (s *Store) ReplaceEntry(e *Entry) error {
	_, err := s.db.Exec(`UPDATE entries SET author_uri = ?, fetched_from = ?, feed_id = ?, in_reply_to_ref = ?,
		sort_time = ?, updated = ?, verified = ?, xml = ?, received = ? WHERE n = ?`,
		e.AuthorURI, e.FetchedFrom, e.FeedID, e.InReplyToRef, e.SortTime, e.Updated, e.Verified, e.XML, e.Received, e.N)
	return err
}

// CountReplies returns the number of direct replies to the entry with the
// given Atom id.
func (s *Store) CountReplies(id string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM entries WHERE in_reply_to_ref = ?`, id).Scan(&n)
	return n, err
}

// Replies returns the direct replies to the entry with the given Atom id,
// oldest first.
func (s *Store) Replies(id string) ([]*Entry, error) {
	rows, err := s.db.Query(`SELECT `+EntryColumns+` FROM entries WHERE in_reply_to_ref = ?
		ORDER BY sort_time, n`, id)
	if err != nil {
		return nil, err
	}
	return ScanEntries(rows)
}
