package sign

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"

	"atomicmicro.blog/internal/atom"
)

// KeyCache fetches and remembers key documents per author URI. It is safe
// for concurrent use.
type KeyCache struct {
	client *http.Client

	mu   sync.Mutex
	docs map[string]*KeyDoc
}

func NewKeyCache(client *http.Client) *KeyCache {
	return &KeyCache{client: client, docs: map[string]*KeyDoc{}}
}

// Verify checks the entry's signature against its author's keys. A key id
// missing from the cached document triggers one re-fetch, so rotated keys are
// picked up.
func (kc *KeyCache) Verify(e *atom.Entry) error {
	if e.Signature == nil {
		return ErrUnsigned
	}
	if e.Author == nil || e.Author.URI == "" {
		return errors.New("sign: entry has no author uri")
	}
	doc, err := kc.get(e.Author.URI, false)
	if err != nil {
		return err
	}
	if _, ok := doc.Find(e.Signature.KeyID); !ok {
		if doc, err = kc.get(e.Author.URI, true); err != nil {
			return err
		}
	}
	return Verify(e, doc)
}

func (kc *KeyCache) get(authorURI string, refresh bool) (*KeyDoc, error) {
	kc.mu.Lock()
	doc, ok := kc.docs[authorURI]
	kc.mu.Unlock()
	if ok && !refresh {
		return doc, nil
	}
	doc, err := kc.fetch(authorURI)
	if err != nil {
		return nil, err
	}
	kc.mu.Lock()
	kc.docs[authorURI] = doc
	kc.mu.Unlock()
	return doc, nil
}

func (kc *KeyCache) fetch(authorURI string) (*KeyDoc, error) {
	url, err := KeysURL(authorURI)
	if err != nil {
		return nil, err
	}
	resp, err := kc.client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	var doc KeyDoc
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&doc); err != nil {
		return nil, fmt.Errorf("GET %s: %w", url, err)
	}
	return &doc, nil
}
