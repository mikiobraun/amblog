package sign

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// The cache re-fetches the key document once when it doesn't know a key id,
// so key rotation is picked up.
func TestKeyCacheRotation(t *testing.T) {
	priv1, doc := testKeys(t)
	var fetches atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/"+KeysPath {
			http.NotFound(w, r)
			return
		}
		fetches.Add(1)
		json.NewEncoder(w).Encode(doc)
	}))
	defer ts.Close()

	kc := NewKeyCache(ts.Client())
	e := testEntry()
	e.Author.URI = ts.URL + "/"
	if err := Sign(e, priv1, "k1"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := kc.Verify(e); err != nil {
			t.Fatal(err)
		}
	}
	if n := fetches.Load(); n != 1 {
		t.Errorf("fetches = %d, want 1 (cached)", n)
	}

	priv2, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	doc.Keys = append(doc.Keys, NewKey("k2", priv2.Public().(ed25519.PublicKey), time.Now()))
	if err := Sign(e, priv2, "k2"); err != nil {
		t.Fatal(err)
	}
	if err := kc.Verify(e); err != nil {
		t.Fatalf("rotated key: %v", err)
	}
	if n := fetches.Load(); n != 2 {
		t.Errorf("fetches = %d, want 2 (one re-fetch)", n)
	}

	e.Signature = nil
	if err := kc.Verify(e); !errors.Is(err, ErrUnsigned) {
		t.Errorf("unsigned: got %v, want ErrUnsigned", err)
	}
}
