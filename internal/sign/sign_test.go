package sign

import (
	"crypto/ed25519"
	"errors"
	"strings"
	"testing"
	"time"

	"atomicmicro.blog/internal/atom"
)

func testEntry() *atom.Entry {
	return &atom.Entry{
		ID:        "tag:example.com,2026-10:post-1",
		Published: "2026-10-21T12:00:00Z",
		Updated:   "2026-10-21T12:00:00Z",
		Author:    &atom.Person{Name: "Ex", URI: "https://example.com/"},
		Content:   &atom.Content{Type: "html", Body: "<p>a\\b</p>\nline2\r"},
		InReplyTo: &atom.InReplyTo{Ref: "tag:other.example,2026-10:post-9", Href: "https://other.example/posts/9"},
		Links: []atom.Link{
			{Rel: "alternate", Href: "https://example.com/posts/1"},
			{Rel: "enclosure", Href: "https://example.com/a.jpg", Type: "image/jpeg"},
			{Rel: "enclosure", Href: "https://example.com/b.jpg", Type: "image/jpeg"},
		},
	}
}

func testKeys(t *testing.T) (ed25519.PrivateKey, *KeyDoc) {
	t.Helper()
	priv, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	doc := &KeyDoc{Keys: []Key{NewKey("k1", priv.Public().(ed25519.PublicKey), time.Now())}}
	return priv, doc
}

func TestInput(t *testing.T) {
	got, err := Input(testEntry())
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		`id: tag:example.com,2026-10:post-1`,
		`author: https://example.com/`,
		`published: 2026-10-21T12:00:00Z`,
		`updated: 2026-10-21T12:00:00Z`,
		`in-reply-to: tag:other.example,2026-10:post-9`,
		`verb: `,
		`title: `,
		`summary: `,
		`content-type: html`,
		`content: <p>a\\b</p>\nline2\r`,
		`enclosure: https://example.com/a.jpg`,
		`enclosure: https://example.com/b.jpg`,
	}, "\n")
	if string(got) != want {
		t.Errorf("signing input mismatch\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestInputRejects(t *testing.T) {
	e := testEntry()
	e.Content.Type = "xhtml"
	if _, err := Input(e); err == nil {
		t.Error("xhtml content: want error")
	}
	e = testEntry()
	e.Author = nil
	if _, err := Input(e); err == nil {
		t.Error("missing author: want error")
	}
}

// Signatures must survive serializing to XML and parsing back.
func TestSignVerifyRoundTrip(t *testing.T) {
	priv, keys := testKeys(t)
	e := testEntry()
	e.Content.Body = "<p>ümlauts & \"quotes\" 🦫</p>\r\n  indented\n"
	if err := Sign(e, priv, "k1"); err != nil {
		t.Fatal(err)
	}
	data, err := atom.MarshalEntry(e)
	if err != nil {
		t.Fatal(err)
	}
	f, err := atom.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	parsed := f.Entries[0]
	if parsed.Content.Body != e.Content.Body {
		t.Fatalf("content changed in round trip: %q -> %q", e.Content.Body, parsed.Content.Body)
	}
	if err := Verify(parsed, keys); err != nil {
		t.Fatalf("verify after round trip: %v\n%s", err, data)
	}
}

func TestVerifyTampered(t *testing.T) {
	priv, keys := testKeys(t)
	e := testEntry()
	if err := Sign(e, priv, "k1"); err != nil {
		t.Fatal(err)
	}
	e.Content.Body = "something else"
	if err := Verify(e, keys); !errors.Is(err, ErrBadSignature) {
		t.Errorf("tampered content: got %v, want ErrBadSignature", err)
	}
}

// Fields outside the signing input, like links, may change freely.
func TestVerifyUnsignedFieldsChange(t *testing.T) {
	priv, keys := testKeys(t)
	e := testEntry()
	if err := Sign(e, priv, "k1"); err != nil {
		t.Fatal(err)
	}
	e.Links[0].Href = "https://agg.example/posts/abc"
	e.Links = append(e.Links, atom.Link{Rel: "via", Href: "https://example.com/posts/1"})
	if err := Verify(e, keys); err != nil {
		t.Errorf("changed alternate link: %v", err)
	}
}

func TestKeysURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://miki.one/":      "https://miki.one/.well-known/atomic-keys",
		"https://miki.one":       "https://miki.one/.well-known/atomic-keys",
		"http://localhost:8100/": "http://localhost:8100/.well-known/atomic-keys",
	} {
		got, err := KeysURL(in)
		if err != nil || got != want {
			t.Errorf("KeysURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := KeysURL("/relative"); err == nil {
		t.Error("relative author uri: want error")
	}
}
