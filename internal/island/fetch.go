package island

import (
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

const MaxFetchSize = 10 << 20

// Response is a fetched HTTP response, body included.
type Response struct {
	Status       int
	ContentType  string
	ETag         string
	LastModified string
	Body         []byte
}

// Fetch GETs url with the given extra headers. Any status is returned as is;
// only transport errors are errors. Bodies larger than MaxFetchSize fail.
func Fetch(client *http.Client, url string, header http.Header) (*Response, error) {
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
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxFetchSize+1))
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", url, err)
	}
	if len(body) > MaxFetchSize {
		return nil, fmt.Errorf("GET %s: response larger than %d bytes", url, MaxFetchSize)
	}
	return &Response{
		Status:       resp.StatusCode,
		ContentType:  resp.Header.Get("Content-Type"),
		ETag:         resp.Header.Get("ETag"),
		LastModified: resp.Header.Get("Last-Modified"),
		Body:         body,
	}, nil
}

var acceptFeed = http.Header{"Accept": {atom.MediaType + ", text/html;q=0.5"}}

// DiscoverFeed resolves a URL that is either an Atom feed or an HTML page
// linking one (<link rel="alternate" type="application/atom+xml">), and
// returns the feed's URL and contents.
func DiscoverFeed(client *http.Client, pageURL string) (feedURL string, f *atom.Feed, err error) {
	resp, err := Fetch(client, pageURL, acceptFeed)
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
	resp, err = Fetch(client, feedURL, acceptFeed)
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

// CheckFetchURL accepts absolute http(s) URLs only.
func CheckFetchURL(s string) (*url.URL, error) {
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("%q is not an absolute http(s) URL", s)
	}
	return u, nil
}

func IsLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// LinkHref returns the href of the first link with the given rel; a link
// without rel counts as alternate.
func LinkHref(links []atom.Link, rel string) string {
	for _, l := range links {
		if l.Rel == rel || (l.Rel == "" && rel == "alternate") {
			return l.Href
		}
	}
	return ""
}

func resolve(base, ref string) (string, error) {
	b, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	r, err := url.Parse(ref)
	if err != nil {
		return "", err
	}
	return b.ResolveReference(r).String(), nil
}
