package amagg

import (
	"net/http"
	"sync"
	"time"

	"atomicmicro.blog/internal/atom"
	"atomicmicro.blog/internal/web"
)

const (
	proxyTTL     = time.Minute
	proxyMaxSize = 256 // cached feeds; the cache is emptied when full
)

// proxyCache keeps recently proxied feeds, so a client paging through
// profiles doesn't hit remote servers on every view.
type proxyCache struct {
	mu      sync.Mutex
	entries map[string]proxied
}

type proxied struct {
	body    []byte
	expires time.Time
}

func (c *proxyCache) get(url string, now time.Time) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	p, ok := c.entries[url]
	if !ok || !now.Before(p.expires) {
		return nil, false
	}
	return p.body, true
}

func (c *proxyCache) put(url string, body []byte, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil || len(c.entries) >= proxyMaxSize {
		c.entries = map[string]proxied{}
	}
	c.entries[url] = proxied{body: body, expires: now.Add(proxyTTL)}
}

// handleProxy fetches any Atom feed or entry for a member's client, so
// frontends don't need CORS from arbitrary servers. Only responses that parse
// as Atom are passed on, so this is no open proxy.
func (s *Server) handleProxy(w http.ResponseWriter, r *http.Request, u *User) {
	target := r.URL.Query().Get("url")
	if _, err := checkFetchURL(target); err != nil {
		web.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	body, ok := s.proxy.get(target, s.now())
	if !ok {
		resp, err := fetch(s.client, target, http.Header{"Accept": {atom.MediaType}})
		if err != nil {
			web.WriteError(w, http.StatusBadGateway, err.Error())
			return
		}
		if resp.Status != http.StatusOK {
			web.WriteError(w, http.StatusBadGateway, "upstream status "+http.StatusText(resp.Status))
			return
		}
		if _, err := atom.Parse(resp.Body); err != nil {
			web.WriteError(w, http.StatusBadGateway, "not an Atom document: "+err.Error())
			return
		}
		body = resp.Body
		s.proxy.put(target, body, s.now())
	}
	w.Header().Set("Content-Type", atom.MediaType+"; charset=utf-8")
	w.Write(body)
}
