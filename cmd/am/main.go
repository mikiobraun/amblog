// Command am is a command-line client for Atomic Micro Blogging.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const usageText = `usage: am <command> [flags] [args]

  config [-feed URL] [-token TOKEN]    show or set the feed server
  post [-title T] [-html] TEXT...      publish a post (TEXT "-" reads stdin)
  reply [-html] URL TEXT...            reply to the entry at URL
  ls [-n N] [-before CURSOR]           list your posts, most recently updated first
  rm SEQ                               delete a post (leaves a tombstone)
  token create [-expires TIME] NAME    create a posting token (admin)
  show URL                             fetch a feed or entry, verify signatures
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usageText)
		os.Exit(2)
	}
	cmds := map[string]func([]string) error{
		"config": runConfig,
		"post":   runPost,
		"reply":  runReply,
		"ls":     runLs,
		"rm":     runRm,
		"token":  runToken,
		"show":   runShow,
	}
	cmd, ok := cmds[os.Args[1]]
	if !ok {
		fmt.Fprint(os.Stderr, usageText)
		os.Exit(2)
	}
	if err := cmd(os.Args[2:]); err != nil {
		fmt.Fprintln(os.Stderr, "am:", err)
		os.Exit(1)
	}
}

func runConfig(args []string) error {
	fs := flag.NewFlagSet("config", flag.ExitOnError)
	feed := fs.String("feed", "", "feed server base URL")
	token := fs.String("token", "", "feed server token")
	fs.Parse(args)

	cfg, path, err := loadConfig()
	if err != nil {
		return err
	}
	if *feed != "" || *token != "" {
		if *feed != "" {
			cfg.FeedURL = strings.TrimSuffix(*feed, "/")
		}
		if *token != "" {
			cfg.Token = *token
		}
		if err := saveConfig(path, cfg); err != nil {
			return err
		}
	}
	masked := "(none)"
	if cfg.Token != "" {
		masked = cfg.Token[:min(8, len(cfg.Token))] + "…"
	}
	fmt.Printf("config: %s\nfeed:   %s\ntoken:  %s\n", path, cfg.FeedURL, masked)
	return nil
}

type inReplyTo struct {
	Ref  string `json:"ref"`
	Href string `json:"href"`
}

type entry struct {
	Seq         int64      `json:"seq"`
	ID          string     `json:"id"`
	URL         string     `json:"url"`
	Published   string     `json:"published"`
	Updated     string     `json:"updated"`
	Title       string     `json:"title"`
	ContentType string     `json:"content_type"`
	Content     string     `json:"content"`
	InReplyTo   *inReplyTo `json:"in_reply_to"`
	Deleted     bool       `json:"deleted"`
}

type createEntry struct {
	Content     string     `json:"content"`
	ContentType string     `json:"content_type"`
	Title       string     `json:"title,omitempty"`
	InReplyTo   *inReplyTo `json:"in_reply_to,omitempty"`
}

// readText joins args into the post text; a single "-" reads stdin.
func readText(args []string) (string, error) {
	var text string
	if len(args) == 1 && args[0] == "-" {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", err
		}
		text = strings.TrimSuffix(string(data), "\n")
	} else {
		text = strings.Join(args, " ")
	}
	if text == "" {
		return "", errors.New("no text given")
	}
	return text, nil
}

func contentType(isHTML bool) string {
	if isHTML {
		return "html"
	}
	return "text"
}

func publish(req createEntry) error {
	c, err := newFeedClient()
	if err != nil {
		return err
	}
	var e entry
	if err := c.do("POST", "/api/entries", req, &e); err != nil {
		return err
	}
	fmt.Println(e.URL)
	return nil
}

func runPost(args []string) error {
	fs := flag.NewFlagSet("post", flag.ExitOnError)
	title := fs.String("title", "", "title (for long-form posts)")
	isHTML := fs.Bool("html", false, "text is HTML")
	fs.Parse(args)
	text, err := readText(fs.Args())
	if err != nil {
		return err
	}
	return publish(createEntry{Content: text, ContentType: contentType(*isHTML), Title: *title})
}

func runReply(args []string) error {
	fs := flag.NewFlagSet("reply", flag.ExitOnError)
	isHTML := fs.Bool("html", false, "text is HTML")
	fs.Parse(args)
	if fs.NArg() < 2 {
		return errors.New("usage: am reply [-html] URL TEXT...")
	}
	url := fs.Arg(0)
	text, err := readText(fs.Args()[1:])
	if err != nil {
		return err
	}
	doc, err := fetchAtom(url)
	if err != nil {
		return err
	}
	if len(doc.Entries) != 1 {
		return fmt.Errorf("%s: expected a single entry, got %d", url, len(doc.Entries))
	}
	return publish(createEntry{
		Content:     text,
		ContentType: contentType(*isHTML),
		InReplyTo:   &inReplyTo{Ref: doc.Entries[0].ID, Href: url},
	})
}

func runLs(args []string) error {
	fs := flag.NewFlagSet("ls", flag.ExitOnError)
	n := fs.Int("n", 20, "number of posts")
	before := fs.String("before", "", "page cursor, as printed by a previous ls")
	fs.Parse(args)
	c, err := newFeedClient()
	if err != nil {
		return err
	}
	q := url.Values{"limit": {strconv.Itoa(*n)}}
	if *before != "" {
		q.Set("before", *before)
	}
	var resp struct {
		Entries []entry
		Next    string
	}
	if err := c.do("GET", "/api/entries?"+q.Encode(), nil, &resp); err != nil {
		return err
	}
	for _, e := range resp.Entries {
		text := e.Content
		if e.Title != "" {
			text = e.Title
		}
		if e.Deleted {
			text = "(deleted)"
		}
		reply := ""
		if e.InReplyTo != nil {
			reply = "↳ "
		}
		fmt.Printf("%4d  %s  %s%s\n", e.Seq, e.Updated, reply, oneLine(text, 60))
	}
	if resp.Next != "" {
		fmt.Printf("\nmore: am ls -before %s\n", resp.Next)
	}
	return nil
}

func runRm(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: am rm SEQ")
	}
	seq, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		return fmt.Errorf("%q is not a post number", args[0])
	}
	c, err := newFeedClient()
	if err != nil {
		return err
	}
	return c.do("DELETE", fmt.Sprintf("/api/entries/%d", seq), nil, nil)
}

func runToken(args []string) error {
	if len(args) < 1 || args[0] != "create" {
		return errors.New("usage: am token create [-expires TIME] NAME")
	}
	fs := flag.NewFlagSet("token create", flag.ExitOnError)
	expires := fs.String("expires", "", "expiry time, RFC 3339 (e.g. 2027-01-01T00:00:00Z)")
	fs.Parse(args[1:])
	if fs.NArg() != 1 {
		return errors.New("usage: am token create [-expires TIME] NAME")
	}
	req := map[string]any{"name": fs.Arg(0)}
	if *expires != "" {
		t, err := time.Parse(time.RFC3339, *expires)
		if err != nil {
			return fmt.Errorf("-expires: %w", err)
		}
		req["expires"] = t
	}
	c, err := newFeedClient()
	if err != nil {
		return err
	}
	var resp struct{ Token string }
	if err := c.do("POST", "/api/tokens", req, &resp); err != nil {
		return err
	}
	fmt.Println(resp.Token)
	return nil
}

func runShow(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: am show URL")
	}
	f, err := fetchAtom(args[0])
	if err != nil {
		return err
	}
	if f.Title != "" {
		fmt.Printf("%s\n%s\n\n", f.Title, f.ID)
	}
	keys := keyCache{}
	for _, e := range f.Entries {
		status := "✓"
		if err := keys.verify(e); err != nil {
			status = "✗ " + err.Error()
		}
		fmt.Printf("%s %s\n", status, e.ID)
		fmt.Printf("  %s · %s\n", e.LinkHref("alternate"), e.Published)
		if e.InReplyTo != nil {
			fmt.Printf("  ↳ in reply to %s\n", e.InReplyTo.Ref)
		}
		if e.Title != "" {
			fmt.Printf("  # %s\n", e.Title)
		}
		if e.Content != nil {
			for line := range strings.SplitSeq(e.Content.Body, "\n") {
				fmt.Printf("  %s\n", line)
			}
		}
		fmt.Println()
	}
	return nil
}

func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > max {
		return string(r[:max-1]) + "…"
	}
	return s
}
