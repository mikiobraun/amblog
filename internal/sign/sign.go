// Package sign implements entry signing: Ed25519 signatures over a fixed list
// of entry fields (see the spec, "Entry Signing").
package sign

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"atomicmicro.blog/internal/atom"
)

const Algorithm = "ed25519"

var (
	ErrUnsigned     = errors.New("sign: entry is not signed")
	ErrBadSignature = errors.New("sign: signature does not match")
)

var escaper = strings.NewReplacer(`\`, `\\`, "\n", `\n`, "\r", `\r`)

// Input builds the signing input for an entry.
func Input(e *atom.Entry) ([]byte, error) {
	if e.Author == nil || e.Author.URI == "" {
		return nil, errors.New("sign: entry has no author uri")
	}
	contentType, content := "", ""
	if e.Content != nil {
		contentType = e.Content.Type
		if contentType == "" {
			contentType = "text" // RFC 4287 default
		}
		if contentType != "text" && contentType != "html" {
			return nil, fmt.Errorf("sign: content type %q cannot be signed, only text or html", contentType)
		}
		content = e.Content.Body
	}
	inReplyTo := ""
	if e.InReplyTo != nil {
		inReplyTo = e.InReplyTo.Ref
	}

	fields := [][2]string{
		{"id", e.ID},
		{"author", e.Author.URI},
		{"published", e.Published},
		{"updated", e.Updated},
		{"in-reply-to", inReplyTo},
		{"verb", e.Verb},
		{"title", e.Title},
		{"summary", e.Summary},
		{"content-type", contentType},
		{"content", content},
	}
	for _, l := range e.Links {
		if l.Rel == "enclosure" {
			fields = append(fields, [2]string{"enclosure", l.Href})
		}
	}

	lines := make([]string, len(fields))
	for i, f := range fields {
		lines[i] = f[0] + ": " + escaper.Replace(f[1])
	}
	return []byte(strings.Join(lines, "\n")), nil
}

// Sign signs the entry and sets its Signature.
func Sign(e *atom.Entry, key ed25519.PrivateKey, keyID string) error {
	in, err := Input(e)
	if err != nil {
		return err
	}
	e.Signature = &atom.Signature{
		KeyID:     keyID,
		Algorithm: Algorithm,
		Value:     base64.StdEncoding.EncodeToString(ed25519.Sign(key, in)),
	}
	return nil
}

// Verify checks the entry's signature against the author's published keys.
func Verify(e *atom.Entry, keys *KeyDoc) error {
	if e.Signature == nil {
		return ErrUnsigned
	}
	if e.Signature.Algorithm != Algorithm {
		return fmt.Errorf("sign: unsupported algorithm %q", e.Signature.Algorithm)
	}
	key, ok := keys.Find(e.Signature.KeyID)
	if !ok {
		return fmt.Errorf("sign: unknown key id %q", e.Signature.KeyID)
	}
	pub, err := key.Ed25519()
	if err != nil {
		return err
	}
	sig, err := base64.StdEncoding.DecodeString(e.Signature.Value)
	if err != nil {
		return fmt.Errorf("sign: bad signature encoding: %w", err)
	}
	in, err := Input(e)
	if err != nil {
		return err
	}
	if !ed25519.Verify(pub, in, sig) {
		return ErrBadSignature
	}
	return nil
}
