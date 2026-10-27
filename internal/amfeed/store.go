package amfeed

import (
	"database/sql"
	"errors"
	"time"

	"atomicmicro.blog/internal/paging"
	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("not found")

// Entry is a stored entry. Seq is the local post number (/posts/{seq}),
// ID the Atom entry id. Times are RFC 3339 strings, as signed.
//
// Entries made from a file in the content folder have a SourcePath (relative
// to the folder) and the SourceHash they were built from; the content sync
// owns them, the API doesn't change them.
type Entry struct {
	Seq           int64
	ID            string
	Published     string
	Updated       string
	Title         string
	ContentType   string
	Content       string
	InReplyToRef  string
	InReplyToHref string
	Verb          string
	KeyID         string
	Signature     string
	Deleted       bool
	SourcePath    string
	SourceHash    string
}

var schema = []string{
	`PRAGMA journal_mode=WAL`,
	`PRAGMA busy_timeout=5000`,
	`CREATE TABLE IF NOT EXISTS entries (
		seq              INTEGER PRIMARY KEY,
		id               TEXT NOT NULL UNIQUE,
		published        TEXT NOT NULL,
		updated          TEXT NOT NULL,
		title            TEXT NOT NULL,
		content_type     TEXT NOT NULL,
		content          TEXT NOT NULL,
		in_reply_to_ref  TEXT NOT NULL,
		in_reply_to_href TEXT NOT NULL,
		verb             TEXT NOT NULL,
		key_id           TEXT NOT NULL,
		signature        TEXT NOT NULL,
		deleted          INTEGER NOT NULL,
		source_path      TEXT NOT NULL DEFAULT '',
		source_hash      TEXT NOT NULL DEFAULT ''
	)`,
	`CREATE INDEX IF NOT EXISTS entries_feed_order ON entries (updated, seq)`,
	`CREATE TABLE IF NOT EXISTS tokens (
		hash    TEXT PRIMARY KEY,
		name    TEXT NOT NULL,
		admin   INTEGER NOT NULL,
		created TEXT NOT NULL,
		expires TEXT
	)`,
	// WebSub subscribers of the feed (amfeed is its own hub).
	`CREATE TABLE IF NOT EXISTS subscriptions (
		callback TEXT PRIMARY KEY,
		secret   TEXT NOT NULL,
		expires  TEXT NOT NULL
	)`,
	// Received WebMentions: the entry found at source that replies to our
	// post seq, as received (or made up for a plain HTML page).
	`CREATE TABLE IF NOT EXISTS mentions (
		source   TEXT NOT NULL,
		seq      INTEGER NOT NULL,
		updated  TEXT NOT NULL,
		xml      TEXT NOT NULL,
		received TEXT NOT NULL,
		PRIMARY KEY (source, seq)
	)`,
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
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

// migrate brings databases created by older versions up to the schema.
func migrate(db *sql.DB) error {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('entries') WHERE name = 'source_path'`).Scan(&n)
	if err != nil {
		return err
	}
	if n == 0 {
		for _, stmt := range []string{
			`ALTER TABLE entries ADD COLUMN source_path TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE entries ADD COLUMN source_hash TEXT NOT NULL DEFAULT ''`,
		} {
			if _, err := db.Exec(stmt); err != nil {
				return err
			}
		}
	}
	_, err = db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS entries_source ON entries (source_path) WHERE source_path <> ''`)
	return err
}

func (s *Store) Close() error { return s.db.Close() }

const entryColumns = `seq, id, published, updated, title, content_type, content,
	in_reply_to_ref, in_reply_to_href, verb, key_id, signature, deleted, source_path, source_hash`

func scanEntry(row interface{ Scan(...any) error }) (*Entry, error) {
	var e Entry
	err := row.Scan(&e.Seq, &e.ID, &e.Published, &e.Updated, &e.Title, &e.ContentType, &e.Content,
		&e.InReplyToRef, &e.InReplyToHref, &e.Verb, &e.KeyID, &e.Signature, &e.Deleted, &e.SourcePath, &e.SourceHash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &e, err
}

// CreateEntry allocates the next seq and stores the entry that build returns
// for it, in one transaction. build is where the entry gets its id and
// signature, both of which depend on seq.
func (s *Store) CreateEntry(build func(seq int64) (*Entry, error)) (*Entry, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var seq int64
	if err := tx.QueryRow(`SELECT COALESCE(MAX(seq), 0) + 1 FROM entries`).Scan(&seq); err != nil {
		return nil, err
	}
	e, err := build(seq)
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(`INSERT INTO entries (`+entryColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.Seq, e.ID, e.Published, e.Updated, e.Title, e.ContentType, e.Content,
		e.InReplyToRef, e.InReplyToHref, e.Verb, e.KeyID, e.Signature, e.Deleted, e.SourcePath, e.SourceHash)
	if err != nil {
		return nil, err
	}
	return e, tx.Commit()
}

func (s *Store) UpdateEntry(e *Entry) error {
	res, err := s.db.Exec(`UPDATE entries SET updated = ?, title = ?, content_type = ?, content = ?,
		in_reply_to_ref = ?, in_reply_to_href = ?, verb = ?, key_id = ?, signature = ?, deleted = ?,
		source_hash = ?
		WHERE seq = ?`,
		e.Updated, e.Title, e.ContentType, e.Content,
		e.InReplyToRef, e.InReplyToHref, e.Verb, e.KeyID, e.Signature, e.Deleted, e.SourceHash, e.Seq)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) GetEntry(seq int64) (*Entry, error) {
	return scanEntry(s.db.QueryRow(`SELECT `+entryColumns+` FROM entries WHERE seq = ?`, seq))
}

// SourceEntries returns the entries made from content files, deleted ones
// included, by source path.
func (s *Store) SourceEntries() (map[string]*Entry, error) {
	rows, err := s.db.Query(`SELECT ` + entryColumns + ` FROM entries WHERE source_path <> ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entries := map[string]*Entry{}
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		entries[e.SourcePath] = e
	}
	return entries, rows.Err()
}

// ListEntries returns up to limit entries in feed order: most recently
// updated first, ties broken by seq. before = nil starts at the top. next is
// the cursor for the following page, or nil if this is the last one.
//
// Timestamps compare correctly as strings because they are all RFC 3339 UTC
// with second precision.
func (s *Store) ListEntries(before *paging.Cursor, limit int) (entries []*Entry, next *paging.Cursor, err error) {
	query := `SELECT ` + entryColumns + ` FROM entries`
	var args []any
	if before != nil {
		query += ` WHERE (updated, seq) < (?, ?)`
		args = append(args, before.Time, before.Seq)
	}
	query += ` ORDER BY updated DESC, seq DESC LIMIT ?`
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
		next = &paging.Cursor{Time: last.Updated, Seq: last.Seq}
	}
	return entries, next, nil
}

// LatestUpdated returns the most recent updated time of any entry, or "" if
// there are none.
func (s *Store) LatestUpdated() (string, error) {
	var updated sql.NullString
	err := s.db.QueryRow(`SELECT MAX(updated) FROM entries`).Scan(&updated)
	return updated.String, err
}

func (s *Store) CreateToken(name, hash string, admin bool, expires *time.Time) error {
	var exp sql.NullString
	if expires != nil {
		exp = sql.NullString{String: expires.UTC().Format(time.RFC3339), Valid: true}
	}
	_, err := s.db.Exec(`INSERT INTO tokens (hash, name, admin, created, expires) VALUES (?, ?, ?, ?, ?)`,
		hash, name, admin, time.Now().UTC().Format(time.RFC3339), exp)
	return err
}

// --- WebSub subscriptions ---

type Subscription struct {
	Callback string
	Secret   string
	Expires  string
}

func (s *Store) PutSubscription(sub Subscription) error {
	_, err := s.db.Exec(`INSERT INTO subscriptions (callback, secret, expires) VALUES (?, ?, ?)
		ON CONFLICT (callback) DO UPDATE SET secret = excluded.secret, expires = excluded.expires`,
		sub.Callback, sub.Secret, sub.Expires)
	return err
}

func (s *Store) DeleteSubscription(callback string) error {
	_, err := s.db.Exec(`DELETE FROM subscriptions WHERE callback = ?`, callback)
	return err
}

// Subscriptions returns the subscriptions that haven't expired at now, and
// deletes the expired ones.
func (s *Store) Subscriptions(now string) ([]Subscription, error) {
	if _, err := s.db.Exec(`DELETE FROM subscriptions WHERE expires <= ?`, now); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT callback, secret, expires FROM subscriptions ORDER BY callback`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var subs []Subscription
	for rows.Next() {
		var sub Subscription
		if err := rows.Scan(&sub.Callback, &sub.Secret, &sub.Expires); err != nil {
			return nil, err
		}
		subs = append(subs, sub)
	}
	return subs, rows.Err()
}

func (s *Store) CountSubscriptions() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM subscriptions`).Scan(&n)
	return n, err
}

// --- received WebMentions ---

type Mention struct {
	Source   string
	Seq      int64
	Updated  string
	XML      string
	Received string
}

func (s *Store) PutMention(m Mention) error {
	_, err := s.db.Exec(`INSERT INTO mentions (source, seq, updated, xml, received) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (source, seq) DO UPDATE SET updated = excluded.updated, xml = excluded.xml, received = excluded.received`,
		m.Source, m.Seq, m.Updated, m.XML, m.Received)
	return err
}

func (s *Store) DeleteMention(source string, seq int64) error {
	_, err := s.db.Exec(`DELETE FROM mentions WHERE source = ? AND seq = ?`, source, seq)
	return err
}

// Mentions returns the mentions of post seq, most recently updated first.
func (s *Store) Mentions(seq int64) ([]Mention, error) {
	rows, err := s.db.Query(`SELECT source, seq, updated, xml, received FROM mentions WHERE seq = ?
		ORDER BY updated DESC, source`, seq)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ms []Mention
	for rows.Next() {
		var m Mention
		if err := rows.Scan(&m.Source, &m.Seq, &m.Updated, &m.XML, &m.Received); err != nil {
			return nil, err
		}
		ms = append(ms, m)
	}
	return ms, rows.Err()
}

// CheckToken looks up an unexpired token by hash and reports whether it is
// an admin token. Unknown or expired tokens return ErrNotFound.
func (s *Store) CheckToken(hash string) (admin bool, err error) {
	now := time.Now().UTC().Format(time.RFC3339)
	err = s.db.QueryRow(`SELECT admin FROM tokens WHERE hash = ? AND (expires IS NULL OR expires > ?)`,
		hash, now).Scan(&admin)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNotFound
	}
	return admin, err
}
