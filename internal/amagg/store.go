package amagg

import (
	"database/sql"
	"errors"
	"strings"
	"time"

	"atomicmicro.blog/internal/island"
	"atomicmicro.blog/internal/paging"
)

var (
	ErrNotFound      = island.ErrNotFound
	ErrInvalidInvite = errors.New("invalid or used invite code")
)

// amagg's tables, next to the island's feeds, entries and subscriptions.
var schema = island.Schema{
	Tables: []string{
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
		// Feed reader credentials: HTTP Basic passwords, one per reader.
		`CREATE TABLE IF NOT EXISTS readers (
			id      INTEGER PRIMARY KEY,
			user_id INTEGER NOT NULL REFERENCES users (id),
			name    TEXT NOT NULL,
			hash    TEXT NOT NULL UNIQUE,
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
	},
	// Polled: members' own feeds and feeds someone follows.
	WantedFeeds: `SELECT feed_id FROM users UNION SELECT feed_id FROM follows`,
	MemberFeeds: `SELECT feed_id FROM users`,
}

// Store is the island's store plus members, tokens, readers, invites and
// follows.
type Store struct {
	*island.Store
	db *sql.DB
}

func OpenStore(path string) (*Store, error) {
	is, err := island.OpenStore(path, schema)
	if err != nil {
		return nil, err
	}
	return &Store{Store: is, db: is.DB()}, nil
}

func nowString() string { return paging.FormatTime(time.Now()) }

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

type Reader struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Created string `json:"created"`
}

func (s *Store) CreateReader(userID int64, name, hash string) (*Reader, error) {
	created := nowString()
	res, err := s.db.Exec(`INSERT INTO readers (user_id, name, hash, created) VALUES (?, ?, ?, ?)`,
		userID, name, hash, created)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	return &Reader{ID: id, Name: name, Created: created}, err
}

func (s *Store) Readers(userID int64) ([]*Reader, error) {
	rows, err := s.db.Query(`SELECT id, name, created FROM readers WHERE user_id = ? ORDER BY id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	readers := []*Reader{}
	for rows.Next() {
		var r Reader
		if err := rows.Scan(&r.ID, &r.Name, &r.Created); err != nil {
			return nil, err
		}
		readers = append(readers, &r)
	}
	return readers, rows.Err()
}

// DeleteReader revokes a reader credential; ErrNotFound if the user has none
// with that id.
func (s *Store) DeleteReader(userID, id int64) error {
	res, err := s.db.Exec(`DELETE FROM readers WHERE user_id = ? AND id = ?`, userID, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// UserByReader returns the user with the given name whose reader password
// hashes to hash, or ErrNotFound.
func (s *Store) UserByReader(name, hash string) (*User, error) {
	return scanUser(s.db.QueryRow(`SELECT `+prefixed("u.", userColumns)+` FROM users u
		JOIN readers r ON r.user_id = u.id WHERE u.name = ? AND r.hash = ?`, name, hash))
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

func (s *Store) Following(userID int64) ([]*island.Feed, error) {
	rows, err := s.db.Query(`SELECT `+prefixed("f.", island.FeedColumns)+` FROM feeds f
		JOIN follows fo ON fo.feed_id = f.id WHERE fo.user_id = ? ORDER BY fo.created`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var feeds []*island.Feed
	for rows.Next() {
		f, err := island.ScanFeed(rows)
		if err != nil {
			return nil, err
		}
		feeds = append(feeds, f)
	}
	return feeds, rows.Err()
}

// --- timelines ---

// Timeline returns up to limit entries, newest first by sort_time, from the
// island (all members' feeds) plus, if userID != 0, that user's follows.
// next is the cursor for the following page, or nil.
func (s *Store) Timeline(userID int64, before *paging.Cursor, limit int) (entries []*island.Entry, next *paging.Cursor, err error) {
	query := `SELECT ` + island.EntryColumns + ` FROM entries WHERE feed_id IN (SELECT feed_id FROM users`
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
	if entries, err = island.ScanEntries(rows); err != nil {
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
