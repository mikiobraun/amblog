package amfeed

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

// contentScanInterval is how often the content folder is checked for changes.
const contentScanInterval = time.Minute

// markdown renders content files. Raw HTML in them is left out (goldmark's
// default), so a file can't smuggle scripts into the feed.
var markdown = goldmark.New(goldmark.WithExtensions(extension.GFM))

// RunContentSync syncs the content folder now and then every
// contentScanInterval, until ctx is done. Errors are logged.
func (s *Server) RunContentSync(ctx context.Context) {
	if s.cfg.Content == nil {
		return
	}
	for {
		if err := s.SyncContent(); err != nil {
			log.Printf("content sync: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(contentScanInterval):
		}
	}
}

// SyncContent makes the posts from the content folder match its files (see
// the spec, "Content Folder"): a new file becomes a post, a changed file
// updates its post, a removed file turns its post into a tombstone.
// Subscribers get one push if anything changed. Files that can't be read or
// parsed are logged and skipped.
func (s *Server) SyncContent() error {
	if s.cfg.Content == nil {
		return nil
	}
	s.syncMu.Lock()
	defer s.syncMu.Unlock()
	changed := 0
	defer func() {
		if changed > 0 {
			s.background(s.publish)
		}
	}()
	// New posts get their post numbers in the first pass; the second pass
	// turns wikilinks to them into links.
	for pass := 0; pass < 2; pass++ {
		created, n, err := s.syncPass()
		changed += n
		if err != nil {
			return err
		}
		if created == 0 {
			break
		}
	}
	return nil
}

// syncPass goes over the folder once. It returns how many posts it created,
// and how many it created, updated or deleted in all.
func (s *Server) syncPass() (created, changed int, err error) {
	files, err := s.contentFiles()
	if err != nil {
		return 0, 0, err
	}
	stored, err := s.store.SourceEntries()
	if err != nil {
		return 0, 0, err
	}
	links := s.linkTargets(files, stored)
	now := s.now().UTC().Format(time.RFC3339)

	present := map[string]bool{}
	for _, rel := range files {
		present[rel] = true
		src, err := os.ReadFile(filepath.Join(s.cfg.Content.Dir, filepath.FromSlash(rel)))
		if err != nil {
			s.skipFile(rel, err)
			continue
		}
		doc, err := parseContent(rel, string(src), links)
		if err != nil {
			s.skipFile(rel, err)
			continue
		}
		delete(s.skipped, rel)
		switch e := stored[rel]; {
		case e == nil:
			if err := s.createFromFile(rel, doc, now); err != nil {
				s.skipFile(rel, err)
				continue
			}
			created++
			changed++
		case e.SourceHash != doc.hash:
			e.Title = doc.title
			e.ContentType = "html"
			e.Content = doc.html
			e.Updated = now
			e.Deleted = false
			e.SourceHash = doc.hash
			if err := s.sign(e); err != nil {
				return created, changed, err
			}
			if err := s.store.UpdateEntry(e); err != nil {
				return created, changed, err
			}
			changed++
		}
	}
	for rel, e := range stored {
		if present[rel] || e.Deleted {
			continue
		}
		e.SourceHash = "" // a file that comes back differs from the tombstone
		if err := s.tombstone(e); err != nil {
			return created, changed, err
		}
		changed++
	}
	return created, changed, nil
}

// skipFile logs why a file was skipped, once until the reason changes, so a
// broken file doesn't fill the log every scan.
func (s *Server) skipFile(rel string, err error) {
	if s.skipped[rel] == err.Error() {
		return
	}
	s.skipped[rel] = err.Error()
	log.Printf("content %s: %v (skipped)", rel, err)
}

func (s *Server) createFromFile(rel string, doc *contentDoc, now string) error {
	published := doc.published
	if published == "" {
		published = now
	}
	t, err := time.Parse(time.RFC3339, published)
	if err != nil {
		return err
	}
	_, err = s.store.CreateEntry(func(seq int64) (*Entry, error) {
		e := &Entry{
			Seq:         seq,
			ID:          doc.id,
			Published:   published,
			Updated:     now,
			Title:       doc.title,
			ContentType: "html",
			Content:     doc.html,
			SourcePath:  rel,
			SourceHash:  doc.hash,
		}
		if e.ID == "" {
			name := (&url.URL{Path: strings.TrimSuffix(rel, path.Ext(rel))}).EscapedPath()
			e.ID = fmt.Sprintf("%s%s:%scontent/%s", s.idPrefix, t.Format("2006-01"), s.idPath, name)
		}
		return e, s.sign(e)
	})
	return err
}

// contentFiles lists the files matching the pattern, as slash-separated
// paths relative to the folder, sorted. Hidden files are left out.
func (s *Server) contentFiles() ([]string, error) {
	dir := s.cfg.Content.Dir
	// Glob finds nothing in a missing folder, which would delete every post.
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("content folder %s is missing", dir)
	}
	matches, err := filepath.Glob(filepath.Join(dir, s.cfg.Content.Pattern))
	if err != nil {
		return nil, err
	}
	var files []string
	for _, m := range matches {
		rel, err := filepath.Rel(dir, m)
		if err != nil {
			return nil, err
		}
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, ".") || strings.Contains(rel, "/.") {
			continue
		}
		if info, err := os.Stat(m); err != nil || !info.Mode().IsRegular() {
			continue
		}
		files = append(files, rel)
	}
	return files, nil
}

// linkTargets maps the names wikilinks use (the file name without extension,
// lowercased) to post URLs, for the files that have a post. A name that
// several files share links nowhere.
func (s *Server) linkTargets(files []string, stored map[string]*Entry) map[string]string {
	targets := map[string]string{}
	count := map[string]int{}
	for _, rel := range files {
		base := path.Base(rel)
		name := strings.ToLower(strings.TrimSuffix(base, path.Ext(base)))
		count[name]++
		if e := stored[rel]; e != nil {
			targets[name] = s.postURL(e.Seq)
		}
	}
	for name, n := range count {
		if n > 1 {
			delete(targets, name)
		}
	}
	return targets
}

// contentDoc is a content file, ready to become an entry. hash covers
// everything the entry is built from, so an unchanged hash means an
// unchanged entry.
type contentDoc struct {
	id        string // from the frontmatter; "" means derive it from the path
	title     string
	published string // RFC 3339, or "" if the frontmatter has no date
	html      string
	hash      string
}

func parseContent(rel, src string, links map[string]string) (*contentDoc, error) {
	fields, front, body, err := splitFrontmatter(src)
	if err != nil {
		return nil, err
	}
	doc := &contentDoc{id: fields["id"], title: fields["title"]}
	if doc.id != "" && !strings.Contains(doc.id, ":") {
		return nil, fmt.Errorf("id %q is not a URI (e.g. tag:example.com,2026:my-post)", doc.id)
	}
	if d := fields["date"]; d != "" {
		if doc.published, err = parseDate(d); err != nil {
			return nil, err
		}
	}
	if doc.title == "" {
		doc.title, body = leadingHeading(body)
		doc.title = wikilinkRE.ReplaceAllStringFunc(doc.title, func(m string) string { return wikilink(m, nil) })
	}
	if doc.title == "" {
		base := path.Base(rel)
		doc.title = strings.TrimSuffix(base, path.Ext(base))
	}
	body = wikilinkRE.ReplaceAllStringFunc(body, func(m string) string { return wikilink(m, links) })
	sum := sha256.Sum256([]byte(front + "\x00" + doc.title + "\x00" + body))
	doc.hash = hex.EncodeToString(sum[:])

	var buf bytes.Buffer
	if err := markdown.Convert([]byte(body), &buf); err != nil {
		return nil, err
	}
	doc.html = buf.String()
	return doc, nil
}

// wikilinkRE matches [[target]], [[target#heading]], [[target|label]], and
// embeds (![[file]]).
var wikilinkRE = regexp.MustCompile(`(!?)\[\[([^\[\]|#]*)(#[^\[\]|]*)?(?:\|([^\[\]]*))?\]\]`)

// wikilink turns one wikilink into a Markdown link to the post it names, or
// into its plain label if no post has that name. Embeds become their label.
func wikilink(m string, links map[string]string) string {
	g := wikilinkRE.FindStringSubmatch(m)
	target := strings.TrimSpace(g[2])
	label := strings.TrimSpace(g[4])
	if label == "" {
		label = target
	}
	if label == "" {
		label = strings.TrimSpace(strings.TrimPrefix(g[3], "#"))
	}
	name := strings.ToLower(strings.TrimSuffix(path.Base(target), ".md"))
	u, ok := links[name]
	if g[1] != "" || target == "" || !ok {
		return label
	}
	return "[" + label + "](" + u + ")"
}

// leadingHeading takes a "# Heading" on the body's first non-blank line as
// the title, and removes it from the body.
func leadingHeading(body string) (title, rest string) {
	rest = body
	for {
		line, after, found := strings.Cut(rest, "\n")
		if strings.TrimSpace(line) != "" {
			h, ok := strings.CutPrefix(strings.TrimRight(line, "\r"), "# ")
			if !ok || strings.TrimSpace(h) == "" {
				return "", body
			}
			return strings.TrimSpace(h), after
		}
		if !found {
			return "", body
		}
		rest = after
	}
}

// parseDate reads a frontmatter date: a day (2026-10-27, taken as midnight
// UTC) or an RFC 3339 time.
func parseDate(v string) (string, error) {
	if t, err := time.Parse("2006-01-02", v); err == nil {
		return t.Format(time.RFC3339), nil
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return "", fmt.Errorf("date %q: want YYYY-MM-DD or an RFC 3339 time", v)
	}
	return t.UTC().Format(time.RFC3339), nil
}

// frontmatterKeys are the frontmatter keys amfeed reads. The rest of the
// frontmatter belongs to other tools and is ignored.
var frontmatterKeys = map[string]bool{"id": true, "title": true, "date": true}

// splitFrontmatter splits a leading frontmatter block (between "---" lines)
// from the body, and reads the top-level `key: value` lines of
// frontmatterKeys. It is not a YAML parser: it reads plain and quoted
// scalars, and rejects anything else for those keys.
func splitFrontmatter(src string) (fields map[string]string, front, body string, err error) {
	fields = map[string]string{}
	src = strings.TrimPrefix(src, string(rune(0xFEFF))) // byte order mark
	if !strings.HasPrefix(src, "---\n") && !strings.HasPrefix(src, "---\r\n") {
		return fields, "", src, nil
	}
	lines := strings.SplitAfter(src, "\n")
	for i := 1; i < len(lines); i++ {
		line := strings.TrimRight(lines[i], "\r\n")
		if line == "---" || line == "..." {
			return fields, strings.Join(lines[:i+1], ""), strings.Join(lines[i+1:], ""), nil
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok || !frontmatterKeys[key] {
			continue // other keys, nested values, list items, comments
		}
		v, err := yamlScalar(strings.TrimSpace(value))
		if err != nil {
			return nil, "", "", fmt.Errorf("frontmatter %s: %w", key, err)
		}
		fields[key] = v
	}
	return nil, "", "", errors.New("frontmatter has no closing ---")
}

func yamlScalar(v string) (string, error) {
	switch {
	case v == "":
		return "", nil
	case v[0] == '"':
		s, err := strconv.Unquote(v)
		if err != nil {
			return "", fmt.Errorf("can't read the quoted string %s", v)
		}
		return s, nil
	case v[0] == '\'':
		if len(v) < 2 || v[len(v)-1] != '\'' {
			return "", fmt.Errorf("can't read the quoted string %s", v)
		}
		return strings.ReplaceAll(v[1:len(v)-1], "''", "'"), nil
	case strings.ContainsRune("|>[{&*!", rune(v[0])):
		return "", errors.New("must be a plain or quoted string on one line")
	}
	if i := strings.Index(v, " #"); i >= 0 {
		v = strings.TrimSpace(v[:i])
	}
	return v, nil
}
