package amfeed

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("not found")

// Entry is a stored entry. Seq is the local post number (/posts/{seq}),
// ID the Atom entry id. Times are RFC 3339 strings, as signed.
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
		deleted          INTEGER NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS entries_feed_order ON entries (updated, seq)`,
	`CREATE TABLE IF NOT EXISTS tokens (
		hash    TEXT PRIMARY KEY,
		name    TEXT NOT NULL,
		admin   INTEGER NOT NULL,
		created TEXT NOT NULL,
		expires TEXT
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
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

const entryColumns = `seq, id, published, updated, title, content_type, content,
	in_reply_to_ref, in_reply_to_href, verb, key_id, signature, deleted`

func scanEntry(row interface{ Scan(...any) error }) (*Entry, error) {
	var e Entry
	err := row.Scan(&e.Seq, &e.ID, &e.Published, &e.Updated, &e.Title, &e.ContentType, &e.Content,
		&e.InReplyToRef, &e.InReplyToHref, &e.Verb, &e.KeyID, &e.Signature, &e.Deleted)
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
	_, err = tx.Exec(`INSERT INTO entries (`+entryColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.Seq, e.ID, e.Published, e.Updated, e.Title, e.ContentType, e.Content,
		e.InReplyToRef, e.InReplyToHref, e.Verb, e.KeyID, e.Signature, e.Deleted)
	if err != nil {
		return nil, err
	}
	return e, tx.Commit()
}

func (s *Store) UpdateEntry(e *Entry) error {
	res, err := s.db.Exec(`UPDATE entries SET updated = ?, title = ?, content_type = ?, content = ?,
		in_reply_to_ref = ?, in_reply_to_href = ?, verb = ?, key_id = ?, signature = ?, deleted = ?
		WHERE seq = ?`,
		e.Updated, e.Title, e.ContentType, e.Content,
		e.InReplyToRef, e.InReplyToHref, e.Verb, e.KeyID, e.Signature, e.Deleted, e.Seq)
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

// Cursor marks a position in feed order (updated desc, seq desc). Its text
// form is "<updated>,<seq>", e.g. "2026-10-21T21:07:11Z,3".
type Cursor struct {
	Updated string
	Seq     int64
}

func (c Cursor) String() string { return c.Updated + "," + strconv.FormatInt(c.Seq, 10) }

func ParseCursor(s string) (Cursor, error) {
	updated, seqStr, ok := strings.Cut(s, ",")
	if !ok {
		return Cursor{}, fmt.Errorf("cursor %q: want <updated>,<seq>", s)
	}
	// Timestamps are compared as strings, so only the canonical format we
	// write is accepted.
	t, err := time.Parse(time.RFC3339, updated)
	if err != nil || t.UTC().Format(time.RFC3339) != updated {
		return Cursor{}, fmt.Errorf("cursor %q: updated must be an RFC 3339 UTC timestamp like 2026-10-21T21:07:11Z", s)
	}
	seq, err := strconv.ParseInt(seqStr, 10, 64)
	if err != nil {
		return Cursor{}, fmt.Errorf("cursor %q: bad post number", s)
	}
	return Cursor{Updated: updated, Seq: seq}, nil
}

// ListEntries returns up to limit entries in feed order: most recently
// updated first, ties broken by seq. before = nil starts at the top. next is
// the cursor for the following page, or nil if this is the last one.
//
// Timestamps compare correctly as strings because they are all RFC 3339 UTC
// with second precision.
func (s *Store) ListEntries(before *Cursor, limit int) (entries []*Entry, next *Cursor, err error) {
	query := `SELECT ` + entryColumns + ` FROM entries`
	var args []any
	if before != nil {
		query += ` WHERE (updated, seq) < (?, ?)`
		args = append(args, before.Updated, before.Seq)
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
		next = &Cursor{Updated: last.Updated, Seq: last.Seq}
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
