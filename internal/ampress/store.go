package ampress

import (
	"database/sql"
	"errors"
	"strings"

	"atomicmicro.blog/internal/island"
	"atomicmicro.blog/internal/paging"
)

// ampress's tables, next to the island's feeds, entries and subscriptions.
// blog holds the one feed the blog shows. posts are its top-level posts by
// slug (see the spec, "What's a Post" and "Post URLs"); replies, likes and
// reposts have no row.
var schema = island.Schema{
	Tables: []string{
		`CREATE TABLE IF NOT EXISTS blog (
			id      INTEGER PRIMARY KEY CHECK (id = 1),
			feed_id INTEGER NOT NULL REFERENCES feeds (id)
		)`,
		`CREATE TABLE IF NOT EXISTS posts (
			n       INTEGER PRIMARY KEY REFERENCES entries (n),
			slug    TEXT NOT NULL UNIQUE,
			deleted INTEGER NOT NULL
		)`,
	},
	WantedFeeds: `SELECT feed_id FROM blog`,
	MemberFeeds: `SELECT feed_id FROM blog`,
}

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

// SetBlogFeed makes feedID the blog's feed. Posts of another feed that was
// the blog's before are dropped.
func (s *Store) SetBlogFeed(feedID int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var old int64
	err = tx.QueryRow(`SELECT feed_id FROM blog`).Scan(&old)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil && old == feedID {
		return nil
	}
	if _, err := tx.Exec(`DELETE FROM posts`); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO blog (id, feed_id) VALUES (1, ?)
		ON CONFLICT (id) DO UPDATE SET feed_id = excluded.feed_id`, feedID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) BlogFeed() (int64, error) {
	var id int64
	err := s.db.QueryRow(`SELECT feed_id FROM blog`).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, island.ErrNotFound
	}
	return id, err
}

// Post is a top-level post of the blog: an entry with its slug.
type Post struct {
	*island.Entry
	Slug    string
	Deleted bool
}

var errSlugTaken = errors.New("slug taken")

// SavePost records entry n as a post under slug. A slug that belongs to
// another entry fails with errSlugTaken.
func (s *Store) SavePost(n int64, slug string, deleted bool) error {
	var owner int64
	err := s.db.QueryRow(`SELECT n FROM posts WHERE slug = ?`, slug).Scan(&owner)
	if err == nil && owner != n {
		return errSlugTaken
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO posts (n, slug, deleted) VALUES (?, ?, ?)
		ON CONFLICT (n) DO UPDATE SET slug = excluded.slug, deleted = excluded.deleted`, n, slug, deleted)
	return err
}

// DeletePost removes entry n from the posts, e.g. when it became a reply.
func (s *Store) DeletePost(n int64) error {
	_, err := s.db.Exec(`DELETE FROM posts WHERE n = ?`, n)
	return err
}

const postColumns = `p.slug, p.deleted, `

func scanPost(row interface{ Scan(...any) error }) (*Post, error) {
	var p Post
	var e island.Entry
	err := row.Scan(&p.Slug, &p.Deleted, &e.N, &e.ID, &e.AuthorURI, &e.FetchedFrom, &e.FeedID, &e.InReplyToRef,
		&e.SortTime, &e.Updated, &e.Verified, &e.XML, &e.Received)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, island.ErrNotFound
	}
	p.Entry = &e
	return &p, err
}

var postSelect = `SELECT ` + postColumns + prefixed("e.", island.EntryColumns) + ` FROM posts p JOIN entries e ON e.n = p.n`

func (s *Store) PostBySlug(slug string) (*Post, error) {
	return scanPost(s.db.QueryRow(postSelect+` WHERE p.slug = ?`, slug))
}

func (s *Store) PostByN(n int64) (*Post, error) {
	return scanPost(s.db.QueryRow(postSelect+` WHERE p.n = ?`, n))
}

// Posts returns up to limit posts that aren't deleted, newest first by
// published time, and the cursor for the next page (nil on the last).
func (s *Store) Posts(before *paging.Cursor, limit int) (posts []*Post, next *paging.Cursor, err error) {
	query := postSelect + ` WHERE NOT p.deleted`
	var args []any
	if before != nil {
		query += ` AND (e.sort_time, e.n) < (?, ?)`
		args = append(args, before.Time, before.Seq)
	}
	query += ` ORDER BY e.sort_time DESC, e.n DESC LIMIT ?`
	args = append(args, limit+1) // one extra to know whether there is a next page
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		p, err := scanPost(rows)
		if err != nil {
			return nil, nil, err
		}
		posts = append(posts, p)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	if len(posts) > limit {
		posts = posts[:limit]
		last := posts[limit-1]
		next = &paging.Cursor{Time: last.SortTime, Seq: last.N}
	}
	return posts, next, nil
}

// prefixed qualifies a comma-separated column list with a table alias.
func prefixed(alias, columns string) string {
	cols := strings.Split(columns, ",")
	for i, c := range cols {
		cols[i] = alias + strings.TrimSpace(c)
	}
	return strings.Join(cols, ", ")
}
