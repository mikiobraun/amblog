package amagg

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"atomicmicro.blog/internal/atom"
	"atomicmicro.blog/internal/island"
	"atomicmicro.blog/internal/paging"
	"atomicmicro.blog/internal/sign"
	"atomicmicro.blog/internal/web"
)

const loginTTL = 10 * time.Minute

// pendingLogin is an issued login challenge, kept in memory until verified
// or expired.
type pendingLogin struct {
	authorURI  string
	inviteHash string // "" if none was given
	expires    time.Time
}

type loginRequest struct {
	AuthorURI string `json:"author_uri"`
	Invite    string `json:"invite"`
}

// handleLogin issues a challenge token. The client publishes
// "<token>.<aggregator origin>" at <author URI>/atomic-challenge/<token>.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if !web.DecodeJSON(w, r, &req) {
		return
	}
	authorURI, err := checkIdentityURI(req.AuthorURI)
	if err != nil {
		web.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	p := pendingLogin{authorURI: authorURI, expires: s.now().Add(loginTTL)}
	if req.Invite != "" {
		p.inviteHash = web.HashToken(req.Invite)
	}
	_, err = s.store.UserByAuthor(authorURI)
	switch {
	case err == nil: // existing member: plain login
	case !errors.Is(err, ErrNotFound):
		web.ServerError(w, err)
		return
	case s.cfg.Aggregator.Registration == "invite":
		if p.inviteHash == "" {
			web.WriteError(w, http.StatusForbidden, "this aggregator is invite-only; an invite code is required to register")
			return
		}
		ok, err := s.store.InviteUsable(p.inviteHash)
		if err != nil {
			web.ServerError(w, err)
			return
		}
		if !ok {
			web.WriteError(w, http.StatusForbidden, ErrInvalidInvite.Error())
			return
		}
	}

	token := web.RandomString(32)
	s.mu.Lock()
	for t, l := range s.logins {
		if !s.now().Before(l.expires) {
			delete(s.logins, t)
		}
	}
	s.logins[token] = p
	s.mu.Unlock()
	web.WriteJSON(w, http.StatusOK, map[string]string{
		"token":   token,
		"expires": paging.FormatTime(p.expires),
	})
}

type loginResponse struct {
	Token       string `json:"token"`
	Name        string `json:"name"`
	AuthorURI   string `json:"author_uri"`
	FeedURL     string `json:"feed_url"`
	TimelineURL string `json:"timeline_url"`
}

// handleLoginVerify checks the published challenge and returns a session
// token, registering the member on first login.
func (s *Server) handleLoginVerify(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token string `json:"token"`
	}
	if !web.DecodeJSON(w, r, &req) {
		return
	}
	s.mu.Lock()
	p, ok := s.logins[req.Token]
	delete(s.logins, req.Token) // single use, whatever the outcome
	s.mu.Unlock()
	if !ok || !s.now().Before(p.expires) {
		web.WriteError(w, http.StatusBadRequest, "unknown or expired login token; request a new one")
		return
	}
	if err := s.checkChallenge(p.authorURI, req.Token); err != nil {
		web.WriteError(w, http.StatusForbidden, err.Error())
		return
	}

	user, err := s.store.UserByAuthor(p.authorURI)
	if errors.Is(err, ErrNotFound) {
		user, err = s.register(p)
		if errors.Is(err, ErrInvalidInvite) {
			web.WriteError(w, http.StatusForbidden, err.Error())
			return
		}
		var re registrationError
		if errors.As(err, &re) {
			web.WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if err != nil {
		web.ServerError(w, err)
		return
	}
	token, hash := web.NewToken("ama_")
	if err := s.store.CreateToken(hash, user.ID); err != nil {
		web.ServerError(w, err)
		return
	}
	f, err := s.store.GetFeed(user.FeedID)
	if err != nil {
		web.ServerError(w, err)
		return
	}
	web.WriteJSON(w, http.StatusOK, loginResponse{
		Token: token, Name: user.Name, AuthorURI: user.AuthorURI,
		FeedURL: f.URL, TimelineURL: s.timelineURL(user),
	})
}

// checkChallenge fetches <author URI>/atomic-challenge/<token> without
// following redirects and compares it with the expected value.
func (s *Server) checkChallenge(authorURI, token string) error {
	u, err := sign.IdentityURL(authorURI, sign.ChallengePath+"/"+token)
	if err != nil {
		return err
	}
	want := token + "." + s.origin
	resp, err := island.Fetch(noRedirects(s.client), u, nil)
	if err != nil {
		return fmt.Errorf("%w: %v", errChallengeMismatch, err)
	}
	if resp.Status != http.StatusOK {
		return fmt.Errorf("%w: GET %s: status %d", errChallengeMismatch, u, resp.Status)
	}
	if got := strings.TrimSpace(string(resp.Body)); got != want {
		return fmt.Errorf("%w: GET %s returned %q, want %q", errChallengeMismatch, u, truncate(got, 100), want)
	}
	return nil
}

type registrationError struct{ error }

// register creates a member: it discovers the feed from the profile page at
// the author URI, checks that the feed speaks for that author, and polls it.
func (s *Server) register(p pendingLogin) (*User, error) {
	feedURL, doc, err := island.DiscoverFeed(s.client, p.authorURI)
	if err != nil {
		return nil, registrationError{err}
	}
	if author := feedAuthorURI(doc); author != "" && normalizeURI(author) != p.authorURI {
		return nil, registrationError{fmt.Errorf("feed %s belongs to %s, not %s", feedURL, author, p.authorURI)}
	}
	f, err := s.island.AddFeed(feedURL)
	if err != nil {
		return nil, err
	}
	user, err := s.store.CreateUser(userName(p.authorURI), p.authorURI, f.ID, p.inviteHash)
	if err != nil {
		return nil, err
	}
	if err := s.island.PollFeed(f); err != nil {
		log.Printf("register %s: first poll of %s: %v", p.authorURI, feedURL, err)
	}
	return user, nil
}

// feedAuthorURI returns the author URI a feed claims: the feed's own author,
// or else its first entry's.
func feedAuthorURI(f *atom.Feed) string {
	if f.Author != nil && f.Author.URI != "" {
		return f.Author.URI
	}
	for _, e := range f.Entries {
		if e.Author != nil && e.Author.URI != "" {
			return e.Author.URI
		}
	}
	return ""
}

// normalizeURI applies the identity normalization of checkIdentityURI
// (empty path → "/") without its other checks.
func normalizeURI(s string) string {
	if n, err := checkIdentityURI(s); err == nil {
		return n
	}
	return s
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
	if u.Scheme != "https" && !island.IsLoopback(u.Hostname()) {
		return "", fmt.Errorf("author uri %q must use https", s)
	}
	if u.Path == "" {
		u.Path = "/"
	}
	return u.String(), nil
}

// noRedirects makes a client return redirect responses instead of following
// them, so a login challenge must be served under the identity URL itself.
func noRedirects(c *http.Client) *http.Client {
	nc := *c
	nc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &nc
}

var errChallengeMismatch = errors.New("challenge not found under the identity URL")

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
