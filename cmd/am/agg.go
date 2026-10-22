package main

import (
	"errors"
	"flag"
	"fmt"
	"net/url"
	"strings"

	"atomicmicro.blog/internal/sign"
)

// runLogin logs in to an aggregator (registering on first login): it asks for
// a challenge, publishes it on the feed server, and has the aggregator check it.
func runLogin(args []string) error {
	fs := flag.NewFlagSet("login", flag.ExitOnError)
	invite := fs.String("invite", "", "invite code, needed to register on invite-only aggregators")
	fs.Parse(args)
	if fs.NArg() != 1 {
		return errors.New("usage: am login [-invite CODE] AGGREGATOR_URL")
	}
	aggURL := strings.TrimSuffix(fs.Arg(0), "/")
	agg, err := url.Parse(aggURL)
	if err != nil || agg.Host == "" {
		return fmt.Errorf("%q is not an aggregator URL", fs.Arg(0))
	}
	c, err := newFeedClient()
	if err != nil {
		return err
	}
	feed, err := fetchAtom(c.cfg.FeedURL + "/feed.atom")
	if err != nil {
		return err
	}
	if feed.Author == nil || feed.Author.URI == "" {
		return fmt.Errorf("%s/feed.atom has no author uri", c.cfg.FeedURL)
	}

	var ch struct{ Token string }
	req := map[string]string{"author_uri": feed.Author.URI, "invite": *invite}
	if err := doJSON("POST", aggURL+"/api/login", "", req, &ch); err != nil {
		return err
	}
	// The value binds the challenge to the aggregator we are talking to.
	value := ch.Token + "." + agg.Scheme + "://" + agg.Host
	if _, err := doRequest("PUT", c.cfg.FeedURL+"/api/challenges/"+ch.Token, c.cfg.Token, "text/plain",
		strings.NewReader(value)); err != nil {
		return err
	}
	var session struct {
		Token       string
		Name        string
		TimelineURL string `json:"timeline_url"`
	}
	if err := doJSON("POST", aggURL+"/api/login/verify", "", map[string]string{"token": ch.Token}, &session); err != nil {
		return err
	}

	cfg, path, err := loadConfig()
	if err != nil {
		return err
	}
	cfg.AggURL, cfg.AggToken, cfg.TimelineURL = aggURL, session.Token, session.TimelineURL
	if err := saveConfig(path, cfg); err != nil {
		return err
	}
	fmt.Printf("Logged in to %s as %s\ntimeline: %s\n", aggURL, session.Name, session.TimelineURL)
	return nil
}

func aggConfig() (*Config, error) {
	cfg, path, err := loadConfig()
	if err != nil {
		return nil, err
	}
	if cfg.AggURL == "" || cfg.AggToken == "" {
		return nil, fmt.Errorf("not logged in to an aggregator (%s); run: am login AGGREGATOR_URL", path)
	}
	return cfg, nil
}

type followed struct {
	FeedURL string `json:"feed_url"`
	Title   string `json:"title"`
}

func runFollow(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: am follow URL   (a feed, or a page linking one)")
	}
	cfg, err := aggConfig()
	if err != nil {
		return err
	}
	var f followed
	if err := doJSON("POST", cfg.AggURL+"/api/follow", cfg.AggToken, map[string]string{"feed_url": args[0]}, &f); err != nil {
		return err
	}
	fmt.Printf("following %s (%s)\n", f.Title, f.FeedURL)
	return nil
}

func runUnfollow(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: am unfollow FEED_URL")
	}
	cfg, err := aggConfig()
	if err != nil {
		return err
	}
	return doJSON("DELETE", cfg.AggURL+"/api/follow?feed_url="+url.QueryEscape(args[0]), cfg.AggToken, nil, nil)
}

func runFollowing(args []string) error {
	cfg, err := aggConfig()
	if err != nil {
		return err
	}
	var resp struct{ Feeds []followed }
	if err := doJSON("GET", cfg.AggURL+"/api/following", cfg.AggToken, nil, &resp); err != nil {
		return err
	}
	for _, f := range resp.Feeds {
		fmt.Printf("%s  %s\n", f.FeedURL, f.Title)
	}
	return nil
}

// runTimeline prints the first page of your timeline, or the island's.
func runTimeline(args []string) error {
	fs := flag.NewFlagSet("timeline", flag.ExitOnError)
	island := fs.Bool("island", false, "the island timeline (all members) instead of yours")
	fs.Parse(args)
	cfg, err := aggConfig()
	if err != nil {
		return err
	}
	u := cfg.TimelineURL
	if *island {
		u = cfg.AggURL + "/timeline.atom"
	}
	f, err := fetchAtomWith(u, cfg.AggToken)
	if err != nil {
		return err
	}
	keys := sign.NewKeyCache(httpClient)
	for _, e := range f.Entries {
		mark := "✓"
		if keys.Verify(e) != nil {
			mark = "?"
		}
		who := ""
		if e.Author != nil {
			who = e.Author.Name
		}
		text := ""
		if e.Content != nil {
			text = e.Content.Body
		}
		if e.Title != "" {
			text = e.Title
		}
		reply := ""
		if e.InReplyTo != nil {
			reply = "↳ "
		}
		fmt.Printf("%s %s  %-12s %s%s\n", mark, e.Published, oneLine(who, 12), reply, oneLine(text, 60))
	}
	return nil
}
