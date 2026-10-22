package amagg

import (
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"atomicmicro.blog/internal/atom"
)

const maxFetchSize = 10 << 20

// response is a fetched HTTP response, body included.
type response struct {
	Status       int
	ContentType  string
	ETag         string
	LastModified string
	Body         []byte
}

// fetch GETs url with the given extra headers. Any status is returned as is;
// only transport errors are errors. Bodies larger than maxFetchSize fail.
func fetch(client *http.Client, url string, header http.Header) (*response, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range header {
		req.Header[k] = v
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxFetchSize+1))
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", url, err)
	}
	if len(body) > maxFetchSize {
		return nil, fmt.Errorf("GET %s: response larger than %d bytes", url, maxFetchSize)
	}
	return &response{
		Status:       resp.StatusCode,
		ContentType:  resp.Header.Get("Content-Type"),
		ETag:         resp.Header.Get("ETag"),
		LastModified: resp.Header.Get("Last-Modified"),
		Body:         body,
	}, nil
}

var acceptFeed = http.Header{"Accept": {atom.MediaType + ", text/html;q=0.5"}}

// discoverFeed resolves a URL that is either an Atom feed or an HTML page
// linking one (<link rel="alternate" type="application/atom+xml">), and
// returns the feed's URL and contents.
func discoverFeed(client *http.Client, pageURL string) (feedURL string, f *atom.Feed, err error) {
	resp, err := fetch(client, pageURL, acceptFeed)
	if err != nil {
		return "", nil, err
	}
	if resp.Status != http.StatusOK {
		return "", nil, fmt.Errorf("GET %s: status %d", pageURL, resp.Status)
	}
	if f, err := atom.Parse(resp.Body); err == nil {
		return pageURL, f, nil
	}
	href := findFeedLink(resp.Body)
	if href == "" {
		return "", nil, fmt.Errorf("%s is neither an Atom feed nor a page linking one", pageURL)
	}
	base, err := url.Parse(pageURL)
	if err != nil {
		return "", nil, err
	}
	ref, err := url.Parse(href)
	if err != nil {
		return "", nil, fmt.Errorf("%s links a bad feed URL %q", pageURL, href)
	}
	feedURL = base.ResolveReference(ref).String()
	resp, err = fetch(client, feedURL, acceptFeed)
	if err != nil {
		return "", nil, err
	}
	if resp.Status != http.StatusOK {
		return "", nil, fmt.Errorf("GET %s: status %d", feedURL, resp.Status)
	}
	f, err = atom.Parse(resp.Body)
	if err != nil {
		return "", nil, fmt.Errorf("%s: %w", feedURL, err)
	}
	return feedURL, f, nil
}

var (
	linkTagRe = regexp.MustCompile(`(?is)<link\s[^>]*>`)
	attrRe    = regexp.MustCompile(`(?is)([a-z][a-z0-9-]*)\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'>]+))`)
)

// findFeedLink returns the href of the first <link rel="alternate"
// type="application/atom+xml"> in an HTML page, or "".
func findFeedLink(page []byte) string {
	for _, tag := range linkTagRe.FindAll(page, -1) {
		attrs := map[string]string{}
		for _, m := range attrRe.FindAllSubmatch(tag, -1) {
			attrs[strings.ToLower(string(m[1]))] = html.UnescapeString(string(m[2]) + string(m[3]) + string(m[4]))
		}
		if hasToken(attrs["rel"], "alternate") && strings.EqualFold(strings.TrimSpace(attrs["type"]), atom.MediaType) && attrs["href"] != "" {
			return attrs["href"]
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

// checkIdentityURI validates and normalizes an author URI used as identity:
// absolute, no query or fragment, HTTPS unless on a loopback host (for
// development). An empty path becomes "/", so "https://miki.one" and
// "https://miki.one/" are the same identity.
func checkIdentityURI(s string) (string, error) {
	u, err := url.Parse(s)
	if err != nil {
		return "", fmt.Errorf("author uri %q: %w", s, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" || u.Host == "" {
		return "", fmt.Errorf("author uri %q is not an absolute http(s) URL", s)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("author uri %q must not have a query or fragment", s)
	}
	if u.Scheme != "https" && !isLoopback(u.Hostname()) {
		return "", fmt.Errorf("author uri %q must use https", s)
	}
	if u.Path == "" {
		u.Path = "/"
	}
	return u.String(), nil
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// noRedirects makes a client return redirect responses instead of following
// them, so a login challenge must be served under the identity URL itself.
func noRedirects(c *http.Client) *http.Client {
	nc := *c
	nc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &nc
}

var errChallengeMismatch = errors.New("challenge not found under the identity URL")
