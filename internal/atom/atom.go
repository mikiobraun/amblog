// Package atom implements the subset of Atom (RFC 4287) and its extensions
// (RFC 4685 threading, the atomic: namespace) used by Atomic Micro Blogging.
//
// Timestamps are kept as the literal RFC 3339 strings from the document,
// since they are part of the signing input and must round-trip unchanged.
package atom

import (
	"encoding/xml"
	"fmt"
)

const (
	NS        = "http://www.w3.org/2005/Atom"
	ThreadNS  = "http://purl.org/syndication/thread/1.0"
	AtomicNS  = "https://atomicmicro.blog/ns/1.0"
	MediaType = "application/atom+xml"

	EntryMediaType = "application/atom+xml;type=entry"
)

type Feed struct {
	XMLName xml.Name `xml:"http://www.w3.org/2005/Atom feed"`
	ID      string   `xml:"id"`
	Title   string   `xml:"title"`
	Updated string   `xml:"updated"`
	Author  *Person  `xml:"author"`
	Links   []Link   `xml:"link"`
	Entries []*Entry `xml:"entry"`
}

type Entry struct {
	XMLName   xml.Name   `xml:"http://www.w3.org/2005/Atom entry"`
	ID        string     `xml:"id"`
	Title     string     `xml:"title"`
	Summary   string     `xml:"summary,omitempty"`
	Published string     `xml:"published,omitempty"`
	Updated   string     `xml:"updated"`
	Author    *Person    `xml:"author"`
	Content   *Content   `xml:"content"`
	Links     []Link     `xml:"link"`
	InReplyTo *InReplyTo `xml:"http://purl.org/syndication/thread/1.0 in-reply-to"`
	// Total is RFC 4685 thr:total, the number of replies the publisher knows
	// (set by aggregators; not signed).
	Total     string     `xml:"http://purl.org/syndication/thread/1.0 total,omitempty"`
	Verb      string     `xml:"https://atomicmicro.blog/ns/1.0 verb,omitempty"`
	Source    *Source    `xml:"source"`
	Signature *Signature `xml:"https://atomicmicro.blog/ns/1.0 signature"`
}

type Person struct {
	Name string `xml:"name"`
	URI  string `xml:"uri,omitempty"`
}

type Link struct {
	Rel    string `xml:"rel,attr,omitempty"`
	Href   string `xml:"href,attr"`
	Type   string `xml:"type,attr,omitempty"`
	Length string `xml:"length,attr,omitempty"`
}

type Content struct {
	Type string `xml:"type,attr,omitempty"`
	Body string `xml:",chardata"`
}

// InReplyTo is the RFC 4685 thr:in-reply-to element.
type InReplyTo struct {
	Ref  string `xml:"ref,attr"`
	Href string `xml:"href,attr,omitempty"`
	Type string `xml:"type,attr,omitempty"`
}

// Source is atom:source, which keeps the origin feed's metadata on an entry
// copied into another feed.
type Source struct {
	ID    string `xml:"id"`
	Title string `xml:"title"`
	Links []Link `xml:"link"`
}

type Signature struct {
	KeyID     string `xml:"key-id,attr"`
	Algorithm string `xml:"algorithm,attr"`
	Value     string `xml:"value,attr"`
}

// LinkHref returns the href of the first link with the given rel, or "".
// A link without rel counts as "alternate", as in RFC 4287.
func (e *Entry) LinkHref(rel string) string {
	for _, l := range e.Links {
		if l.Rel == rel || (l.Rel == "" && rel == "alternate") {
			return l.Href
		}
	}
	return ""
}

func MarshalFeed(f *Feed) ([]byte, error) { return marshal(f) }

func MarshalEntry(e *Entry) ([]byte, error) { return marshal(e) }

func marshal(v any) ([]byte, error) {
	out, err := xml.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return append([]byte(xml.Header), out...), nil
}

// Parse parses an Atom feed or entry document. An entry document is returned
// as a feed containing just that entry.
func Parse(data []byte) (*Feed, error) {
	var root struct{ XMLName xml.Name }
	if err := xml.Unmarshal(data, &root); err != nil {
		return nil, err
	}
	if root.XMLName.Space != NS {
		return nil, fmt.Errorf("atom: not an Atom document (root element %q in namespace %q)", root.XMLName.Local, root.XMLName.Space)
	}
	switch root.XMLName.Local {
	case "feed":
		var f Feed
		if err := xml.Unmarshal(data, &f); err != nil {
			return nil, err
		}
		return &f, nil
	case "entry":
		var e Entry
		if err := xml.Unmarshal(data, &e); err != nil {
			return nil, err
		}
		return &Feed{Entries: []*Entry{&e}}, nil
	default:
		return nil, fmt.Errorf("atom: unexpected root element %q", root.XMLName.Local)
	}
}
