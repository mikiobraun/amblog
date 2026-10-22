// Package webmention implements the parts of WebMention (W3C) that amfeed
// and amagg share: endpoint discovery, sending with retries, checking that a
// source links to its target, and the per-host rate limit on receiving. See
// the spec, "Sending WebMentions" and "Spam Control".
package webmention

import (
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"atomicmicro.blog/internal/atom"
)

const (
	maxBody = 5 << 20

	// DefaultPerHostPerHour is the receiving rate limit when a config sets none.
	DefaultPerHostPerHour = 30
)

var (
	// Retries for Deliver: attempts after the first, and the wait before each.
	retryDelays = []time.Duration{5 * time.Second, 30 * time.Second, 2 * time.Minute}
	// A 429's Retry-After is honored up to this long.
	maxRetryAfter = 10 * time.Minute
)

// ErrNoEndpoint means the target doesn't advertise a WebMention endpoint.
var ErrNoEndpoint = errors.New("no webmention endpoint")

// Discover returns the WebMention endpoint of target: from an HTTP Link
// header, or else the first <link> or <a> with rel="webmention" in an HTML
// page. Relative endpoints are resolved against the (possibly redirected)
// target URL.
func Discover(client *http.Client, target string) (string, error) {
	resp, err := client.Get(target)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET %s: status %d", target, resp.StatusCode)
	}
	base := resp.Request.URL
	href := ""
	for _, h := range resp.Header.Values("Link") {
		if href = linkHeaderRel(h, "webmention"); href != "" {
			break
		}
	}
	if href == "" && strings.Contains(resp.Header.Get("Content-Type"), "html") {
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
		if err != nil {
			return "", err
		}
		for _, attrs := range tags(body, "link", "a") {
			if hasToken(attrs["rel"], "webmention") {
				href = attrs["href"]
				break
			}
		}
	}
	if href == "" {
		return "", ErrNoEndpoint
	}
	ref, err := url.Parse(href)
	if err != nil {
		return "", fmt.Errorf("%s: bad endpoint %q", target, href)
	}
	return base.ResolveReference(ref).String(), nil
}

// RetryError is a 429 answer: try again after the given time.
type RetryError struct{ After time.Duration }

func (e *RetryError) Error() string { return fmt.Sprintf("rate limited, retry after %v", e.After) }

// Send posts one mention to endpoint. Any 2xx is success.
func Send(client *http.Client, endpoint, source, target string) error {
	resp, err := client.PostForm(endpoint, url.Values{"source": {source}, "target": {target}})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, maxBody))
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode == http.StatusTooManyRequests:
		return &RetryError{After: retryAfter(resp.Header.Get("Retry-After"))}
	default:
		return fmt.Errorf("POST %s: status %d", endpoint, resp.StatusCode)
	}
}

// Deliver discovers target's endpoint and sends the mention, retrying failed
// attempts a few times (after Retry-After on a 429). It blocks until done, so
// callers run it in the background. A target without endpoint is not an error.
func Deliver(client *http.Client, source, target string) error {
	var err error
	for attempt := 0; ; attempt++ {
		var endpoint string
		endpoint, err = Discover(client, target)
		if errors.Is(err, ErrNoEndpoint) {
			return nil
		}
		if err == nil {
			if err = Send(client, endpoint, source, target); err == nil {
				return nil
			}
		}
		if attempt >= len(retryDelays) {
			return err
		}
		wait := retryDelays[attempt]
		var re *RetryError
		if errors.As(err, &re) {
			wait = re.After
		}
		time.Sleep(wait)
	}
}

func retryAfter(v string) time.Duration {
	d := time.Minute
	if secs, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && secs >= 0 {
		d = time.Duration(secs) * time.Second
	} else if t, err := http.ParseTime(v); err == nil {
		d = time.Until(t)
	}
	return min(max(d, time.Second), maxRetryAfter)
}

// ParseRequest reads source and target from a WebMention request. Both must
// be absolute http(s) URLs and differ.
func ParseRequest(r *http.Request) (source, target *url.URL, err error) {
	r.Body = http.MaxBytesReader(nil, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		return nil, nil, err
	}
	parse := func(name string) (*url.URL, error) {
		v := r.PostForm.Get(name)
		u, err := url.Parse(v)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, fmt.Errorf("%s must be an absolute http(s) URL", name)
		}
		return u, nil
	}
	if source, err = parse("source"); err != nil {
		return nil, nil, err
	}
	if target, err = parse("target"); err != nil {
		return nil, nil, err
	}
	if source.String() == target.String() {
		return nil, nil, errors.New("source and target must differ")
	}
	return source, target, nil
}

// LinksTo reports whether an HTML page has a link (<a>, <link>) to target.
func LinksTo(page []byte, target string) bool {
	for _, attrs := range tags(page, "a", "link") {
		if attrs["href"] == target {
			return true
		}
	}
	return false
}

// PageEntry makes an entry for a plain HTML page that replies to a post
// (source links to target, the post with Atom id postID): just enough to
// show it as a reply. It is unsigned; its author is the page's host.
func PageEntry(source, target, postID string, page []byte, now string) *atom.Entry {
	host := source
	if u, err := url.Parse(source); err == nil {
		host = u.Host
	}
	return &atom.Entry{
		ID:        source,
		Title:     Title(page),
		Published: now,
		Updated:   now,
		Author:    &atom.Person{Name: host},
		Links:     []atom.Link{{Rel: "alternate", Type: "text/html", Href: source}},
		InReplyTo: &atom.InReplyTo{Ref: postID, Href: target},
	}
}

// Title returns the text of an HTML page's <title>, or "".
func Title(page []byte) string {
	m := titleRe.FindSubmatch(page)
	if m == nil {
		return ""
	}
	return strings.Join(strings.Fields(html.UnescapeString(string(m[1]))), " ")
}

// HostBlocked reports whether host is on the blocklist, itself or as a
// subdomain of a listed host.
func HostBlocked(host string, blocked []string) bool {
	host = strings.ToLower(host)
	for _, b := range blocked {
		b = strings.ToLower(b)
		if host == b || strings.HasSuffix(host, "."+b) {
			return true
		}
	}
	return false
}

// Limiter counts requests per host in fixed one-hour windows.
type Limiter struct {
	perHour int

	mu      sync.Mutex
	windows map[string]*window
}

type window struct {
	start time.Time
	count int
}

func NewLimiter(perHour int) *Limiter {
	return &Limiter{perHour: perHour, windows: map[string]*window{}}
}

// Allow counts a request from host at time now. If the host is over its limit,
// it returns false and how long until the window resets.
func (l *Limiter) Allow(host string, now time.Time) (ok bool, retryAfter time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	w := l.windows[host]
	if w == nil || now.Sub(w.start) >= time.Hour {
		// Drop expired windows now and then, so the map doesn't grow forever.
		if len(l.windows) > 10000 {
			for h, old := range l.windows {
				if now.Sub(old.start) >= time.Hour {
					delete(l.windows, h)
				}
			}
		}
		w = &window{start: now}
		l.windows[host] = w
	}
	if w.count >= l.perHour {
		return false, w.start.Add(time.Hour).Sub(now)
	}
	w.count++
	return true, 0
}

// --- minimal HTML and Link header parsing ---

var (
	tagRe   = regexp.MustCompile(`(?is)<(a|link)\s[^>]*>`)
	attrRe  = regexp.MustCompile(`(?is)([a-z][a-z0-9-]*)\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'>]+))`)
	titleRe = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	linkRe  = regexp.MustCompile(`<([^>]*)>((?:\s*;\s*[^;,]*)*)`)
	relRe   = regexp.MustCompile(`(?i)\brel\s*=\s*(?:"([^"]*)"|([^\s;,"]+))`)
)

// tags returns the attributes of every <a> and <link> tag whose name is one
// of names, in document order.
func tags(page []byte, names ...string) []map[string]string {
	var out []map[string]string
	for _, m := range tagRe.FindAllSubmatch(page, -1) {
		name := strings.ToLower(string(m[1]))
		wanted := false
		for _, n := range names {
			wanted = wanted || n == name
		}
		if !wanted {
			continue
		}
		attrs := map[string]string{}
		for _, a := range attrRe.FindAllSubmatch(m[0], -1) {
			attrs[strings.ToLower(string(a[1]))] = html.UnescapeString(string(a[2]) + string(a[3]) + string(a[4]))
		}
		out = append(out, attrs)
	}
	return out
}

// linkHeaderRel returns the URL of the first link with the given rel in an
// HTTP Link header value, or "".
func linkHeaderRel(header, rel string) string {
	for _, m := range linkRe.FindAllStringSubmatch(header, -1) {
		for _, r := range relRe.FindAllStringSubmatch(m[2], -1) {
			if hasToken(r[1]+r[2], rel) {
				return m[1]
			}
		}
	}
	return ""
}

func hasToken(list, token string) bool {
	for _, t := range strings.Fields(list) {
		if strings.EqualFold(t, token) {
			return true
		}
	}
	return false
}
