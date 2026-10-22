package amfeed

import (
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

// amfeed is its own WebSub hub (see the spec, "WebSub"): subscribers register
// here, and every change pushes the feed's first page to them.

const maxSubscriptions = 1000

var pushRetries = []time.Duration{5 * time.Second, 30 * time.Second}

// handleHub takes subscription requests. The intent is verified in the
// background by calling the subscriber's callback, as WebSub requires.
func (s *Server) handleHub(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	mode := r.PostForm.Get("hub.mode")
	callback := r.PostForm.Get("hub.callback")
	secret := r.PostForm.Get("hub.secret")
	if mode != "subscribe" && mode != "unsubscribe" {
		writeError(w, http.StatusBadRequest, `hub.mode must be "subscribe" or "unsubscribe"`)
		return
	}
	if topic := r.PostForm.Get("hub.topic"); topic != s.feedURL() {
		writeError(w, http.StatusBadRequest, "hub.topic must be "+s.feedURL())
		return
	}
	if u, err := url.Parse(callback); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		writeError(w, http.StatusBadRequest, "hub.callback must be an absolute http(s) URL")
		return
	}
	if len(secret) > websub.MaxSecret {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("hub.secret must be at most %d bytes", websub.MaxSecret))
		return
	}
	var lease time.Duration
	if v := r.PostForm.Get("hub.lease_seconds"); v != "" {
		secs, err := strconv.Atoi(v)
		if err != nil || secs < 0 {
			writeError(w, http.StatusBadRequest, "hub.lease_seconds must be a number of seconds")
			return
		}
		lease = time.Duration(secs) * time.Second
	}
	lease = websub.ClampLease(lease)
	if mode == "subscribe" {
		n, err := s.store.CountSubscriptions()
		if err != nil {
			s.serverError(w, err)
			return
		}
		if n >= maxSubscriptions {
			writeError(w, http.StatusServiceUnavailable, "too many subscriptions")
			return
		}
	}
	w.WriteHeader(http.StatusAccepted)
	s.background(func() {
		if err := s.verifyIntent(mode, callback, secret, lease); err != nil {
			log.Printf("websub %s %s: %v", mode, callback, err)
		}
	})
}

// verifyIntent asks the subscriber to confirm by echoing a challenge, then
// records or removes the subscription.
func (s *Server) verifyIntent(mode, callback, secret string, lease time.Duration) error {
	u, err := url.Parse(callback)
	if err != nil {
		return err
	}
	challenge := web.RandomString(24)
	q := u.Query()
	q.Set("hub.mode", mode)
	q.Set("hub.topic", s.feedURL())
	q.Set("hub.challenge", challenge)
	if mode == "subscribe" {
		q.Set("hub.lease_seconds", strconv.Itoa(int(lease/time.Second)))
	}
	u.RawQuery = q.Encode()
	resp, err := s.client.Get(u.String())
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	if resp.StatusCode < 200 || resp.StatusCode > 299 || string(body) != challenge {
		return fmt.Errorf("callback did not confirm (status %d)", resp.StatusCode)
	}
	if mode == "unsubscribe" {
		return s.store.DeleteSubscription(callback)
	}
	return s.store.PutSubscription(Subscription{
		Callback: callback, Secret: secret, Expires: paging.FormatTime(s.now().Add(lease)),
	})
}

// publish pushes the feed's first page to every subscriber.
func (s *Server) publish() {
	subs, err := s.store.Subscriptions(paging.FormatTime(s.now()))
	if err != nil {
		log.Printf("websub publish: %v", err)
		return
	}
	if len(subs) == 0 {
		return
	}
	body, err := s.feedPage(nil)
	if err != nil {
		log.Printf("websub publish: %v", err)
		return
	}
	for _, sub := range subs {
		s.background(func() { s.push(sub, body) })
	}
}

// push delivers the feed to one subscriber, retrying a few times. A 410
// Gone ends the subscription.
func (s *Server) push(sub Subscription, body []byte) {
	links := strings.Join([]string{"<" + s.hubURL() + `>; rel="hub"`, "<" + s.feedURL() + `>; rel="self"`}, ", ")
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequest("POST", sub.Callback, strings.NewReader(string(body)))
		if err != nil {
			log.Printf("websub push %s: %v", sub.Callback, err)
			return
		}
		req.Header.Set("Content-Type", atom.MediaType+"; charset=utf-8")
		req.Header.Set("Link", links)
		if sub.Secret != "" {
			req.Header.Set("X-Hub-Signature", websub.Signature(sub.Secret, body))
		}
		resp, err := s.client.Do(req)
		if err == nil {
			io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
			resp.Body.Close()
			switch {
			case resp.StatusCode >= 200 && resp.StatusCode < 300:
				return
			case resp.StatusCode == http.StatusGone:
				if err := s.store.DeleteSubscription(sub.Callback); err != nil {
					log.Printf("websub push %s: %v", sub.Callback, err)
				}
				return
			}
			err = fmt.Errorf("status %d", resp.StatusCode)
		}
		if attempt >= len(pushRetries) {
			log.Printf("websub push %s: giving up: %v", sub.Callback, err)
			return
		}
		time.Sleep(pushRetries[attempt])
	}
}
