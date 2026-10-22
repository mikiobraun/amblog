package amagg

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"atomicmicro.blog/internal/atom"
	"atomicmicro.blog/internal/paging"
	"atomicmicro.blog/internal/web"
	"atomicmicro.blog/internal/websub"
)

// The aggregator subscribes via WebSub to every polled feed that names a hub
// (see the spec, "WebSub" and "Polling"). Pushes are ingested like a poll;
// polling continues at max_interval as a fallback.

const (
	renewBefore    = 24 * time.Hour   // renew a subscription this long before it expires
	pendingTimeout = 10 * time.Minute // re-request if the hub hasn't verified by then
)

func (s *Server) callbackURL(feedID int64) string {
	return s.cfg.Aggregator.BaseURL + "/websub/" + strconv.FormatInt(feedID, 10)
}

// ensureSubscription keeps the feed's subscription current: subscribe when
// the feed names a hub, renew before expiry, drop it when the hub is gone.
// Called after a successful poll, with feedMu held.
func (s *Server) ensureSubscription(f *Feed, doc *atom.Feed) error {
	sub, err := s.store.GetSubscription(f.ID)
	if errors.Is(err, ErrNotFound) {
		sub, err = nil, nil
	}
	if err != nil {
		return err
	}
	hub := linkHref(doc.Links, "hub")
	if hub == "" {
		if sub != nil {
			return s.store.DeleteSubscription(f.ID)
		}
		return nil
	}
	if hub, err = resolve(f.URL, hub); err != nil {
		return err
	}
	if _, err := checkFetchURL(hub); err != nil {
		return err
	}
	topic := f.URL
	if self := linkHref(doc.Links, "self"); self != "" {
		if topic, err = resolve(f.URL, self); err != nil {
			return err
		}
	}
	now := s.now()
	if sub != nil && sub.Hub == hub && sub.Topic == topic {
		switch sub.State {
		case "active":
			if exp, err := time.Parse(time.RFC3339, sub.Expires); err == nil && exp.Sub(now) > renewBefore {
				return nil
			}
		case "pending":
			if req, err := time.Parse(time.RFC3339, sub.Requested); err == nil && now.Sub(req) < pendingTimeout {
				return nil
			}
		}
	}
	next := &Subscription{FeedID: f.ID, Hub: hub, Topic: topic, Secret: web.RandomString(24), State: "pending",
		Requested: paging.FormatTime(now)}
	// A renewal keeps the active subscription and its secret: pushes signed
	// with it may arrive until the hub has verified the renewal.
	if sub != nil && sub.Hub == hub && sub.Topic == topic && sub.State == "active" {
		next.Secret, next.State, next.Expires = sub.Secret, sub.State, sub.Expires
	}
	if err := s.store.PutSubscription(next); err != nil {
		return err
	}
	resp, err := s.client.PostForm(hub, url.Values{
		"hub.mode":          {"subscribe"},
		"hub.topic":         {topic},
		"hub.callback":      {s.callbackURL(f.ID)},
		"hub.secret":        {next.Secret},
		"hub.lease_seconds": {strconv.Itoa(int(websub.DefaultLease / time.Second))},
	})
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("subscribe at %s: status %d", hub, resp.StatusCode)
	}
	return nil
}

// pushing reports whether the feed has an active subscription.
func (s *Server) pushing(feedID int64) bool {
	sub, err := s.store.GetSubscription(feedID)
	if err != nil || sub.State != "active" {
		return false
	}
	exp, err := time.Parse(time.RFC3339, sub.Expires)
	return err == nil && s.now().Before(exp)
}

func feedIDParam(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("feed"), 10, 64)
	return id, err == nil
}

// handleWebSubVerify answers the hub's verification of intent: confirm
// subscriptions we asked for, and unsubscriptions of feeds we no longer have.
func (s *Server) handleWebSubVerify(w http.ResponseWriter, r *http.Request) {
	id, ok := feedIDParam(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	q := r.URL.Query()
	sub, err := s.store.GetSubscription(id)
	if err != nil && !errors.Is(err, ErrNotFound) {
		web.ServerError(w, err)
		return
	}
	switch q.Get("hub.mode") {
	case "subscribe":
		if sub == nil || sub.Topic != q.Get("hub.topic") {
			http.NotFound(w, r)
			return
		}
		lease := websub.DefaultLease
		if secs, err := strconv.Atoi(q.Get("hub.lease_seconds")); err == nil && secs > 0 {
			lease = time.Duration(secs) * time.Second
		}
		sub.State, sub.Expires = "active", paging.FormatTime(s.now().Add(lease))
		if err := s.store.PutSubscription(sub); err != nil {
			web.ServerError(w, err)
			return
		}
	case "unsubscribe":
		if sub != nil {
			http.NotFound(w, r) // we still want it
			return
		}
	case "denied":
		if sub != nil {
			log.Printf("websub: hub %s denied feed %d: %s", sub.Hub, id, q.Get("hub.reason"))
			if err := s.store.DeleteSubscription(id); err != nil {
				web.ServerError(w, err)
				return
			}
		}
		w.WriteHeader(http.StatusOK)
		return
	default:
		web.WriteError(w, http.StatusBadRequest, "unknown hub.mode")
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	io.WriteString(w, q.Get("hub.challenge"))
}

// handleWebSubPush ingests content the hub pushed, after checking its
// signature. Feeds nobody wants any more answer 410 Gone, which ends the
// subscription at the hub.
func (s *Server) handleWebSubPush(w http.ResponseWriter, r *http.Request) {
	id, ok := feedIDParam(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxFetchSize))
	if err != nil {
		web.WriteError(w, http.StatusRequestEntityTooLarge, err.Error())
		return
	}
	sub, err := s.store.GetSubscription(id)
	if errors.Is(err, ErrNotFound) {
		w.WriteHeader(http.StatusGone)
		return
	}
	if err != nil {
		web.ServerError(w, err)
		return
	}
	wanted, err := s.store.FeedWanted(id)
	if err != nil {
		web.ServerError(w, err)
		return
	}
	if !wanted {
		if err := s.store.DeleteSubscription(id); err != nil {
			web.ServerError(w, err)
			return
		}
		w.WriteHeader(http.StatusGone)
		return
	}
	// WebSub: content with a bad signature is acknowledged, then ignored.
	if !websub.CheckSignature(sub.Secret, r.Header.Get("X-Hub-Signature"), body) {
		log.Printf("websub push for feed %d: bad signature, ignored", id)
		w.WriteHeader(http.StatusAccepted)
		return
	}
	doc, err := atom.Parse(body)
	if err != nil {
		web.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.ingestPush(id, doc); err != nil {
		web.ServerError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) ingestPush(feedID int64, doc *atom.Feed) error {
	s.feedMu.Lock()
	defer s.feedMu.Unlock()
	f, err := s.store.GetFeed(feedID)
	if err != nil {
		return err
	}
	if _, err := s.ingestPages(f, doc); err != nil {
		return err
	}
	if t := strings.TrimSpace(doc.Title); t != "" {
		f.Title = t
	}
	return s.store.SaveFeed(f)
}
