package island

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

// The island subscribes via WebSub to every polled feed that names a hub
// (see the spec, "WebSub" and "Polling"). Pushes are ingested like a poll;
// polling continues at the maximum interval as a fallback. The application
// routes GET and POST /websub/{feed} to HandleWebSubVerify and
// HandleWebSubPush.

const (
	renewBefore    = 24 * time.Hour   // renew a subscription this long before it expires
	pendingTimeout = 10 * time.Minute // re-request if the hub hasn't verified by then
)

// ensureSubscription keeps the feed's subscription current: subscribe when
// the feed names a hub, renew before expiry, drop it when the hub is gone.
// Called after a successful poll, with feedMu held.
func (is *Island) ensureSubscription(f *Feed, doc *atom.Feed) error {
	sub, err := is.Store.GetSubscription(f.ID)
	if errors.Is(err, ErrNotFound) {
		sub, err = nil, nil
	}
	if err != nil {
		return err
	}
	hub := LinkHref(doc.Links, "hub")
	if hub == "" {
		if sub != nil {
			return is.Store.DeleteSubscription(f.ID)
		}
		return nil
	}
	if hub, err = resolve(f.URL, hub); err != nil {
		return err
	}
	if _, err := CheckFetchURL(hub); err != nil {
		return err
	}
	topic := f.URL
	if self := LinkHref(doc.Links, "self"); self != "" {
		if topic, err = resolve(f.URL, self); err != nil {
			return err
		}
	}
	now := is.Now()
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
	if err := is.Store.PutSubscription(next); err != nil {
		return err
	}
	resp, err := is.Client.PostForm(hub, url.Values{
		"hub.mode":          {"subscribe"},
		"hub.topic":         {topic},
		"hub.callback":      {is.callbackURL(f.ID)},
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

// Pushing reports whether the feed has an active subscription.
func (is *Island) Pushing(feedID int64) bool {
	sub, err := is.Store.GetSubscription(feedID)
	if err != nil || sub.State != "active" {
		return false
	}
	exp, err := time.Parse(time.RFC3339, sub.Expires)
	return err == nil && is.Now().Before(exp)
}

func feedIDParam(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("feed"), 10, 64)
	return id, err == nil
}

// HandleWebSubVerify answers the hub's verification of intent: confirm
// subscriptions we asked for, and unsubscriptions of feeds we no longer have.
func (is *Island) HandleWebSubVerify(w http.ResponseWriter, r *http.Request) {
	id, ok := feedIDParam(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	q := r.URL.Query()
	sub, err := is.Store.GetSubscription(id)
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
		sub.State, sub.Expires = "active", paging.FormatTime(is.Now().Add(lease))
		if err := is.Store.PutSubscription(sub); err != nil {
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
			if err := is.Store.DeleteSubscription(id); err != nil {
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

// HandleWebSubPush ingests content the hub pushed, after checking its
// signature. Feeds nobody wants any more answer 410 Gone, which ends the
// subscription at the hub.
func (is *Island) HandleWebSubPush(w http.ResponseWriter, r *http.Request) {
	id, ok := feedIDParam(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxFetchSize))
	if err != nil {
		web.WriteError(w, http.StatusRequestEntityTooLarge, err.Error())
		return
	}
	sub, err := is.Store.GetSubscription(id)
	if errors.Is(err, ErrNotFound) {
		w.WriteHeader(http.StatusGone)
		return
	}
	if err != nil {
		web.ServerError(w, err)
		return
	}
	wanted, err := is.Store.FeedWanted(id)
	if err != nil {
		web.ServerError(w, err)
		return
	}
	if !wanted {
		if err := is.Store.DeleteSubscription(id); err != nil {
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
	if err := is.ingestPush(id, doc); err != nil {
		web.ServerError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (is *Island) ingestPush(feedID int64, doc *atom.Feed) error {
	is.feedMu.Lock()
	defer is.feedMu.Unlock()
	f, err := is.Store.GetFeed(feedID)
	if err != nil {
		return err
	}
	if _, err := is.ingestPages(f, doc); err != nil {
		return err
	}
	if t := strings.TrimSpace(doc.Title); t != "" {
		f.Title = t
	}
	return is.Store.SaveFeed(f)
}
