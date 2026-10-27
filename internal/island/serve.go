package island

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"

	"atomicmicro.blog/internal/atom"
	"atomicmicro.blog/internal/web"
)

// Atom requires feed/updated; an empty feed has never been updated.
const EmptyFeedUpdated = "1970-01-01T00:00:00Z"

// WriteAtom writes a feed with an ETag, answering If-None-Match with 304.
func WriteAtom(w http.ResponseWriter, r *http.Request, f *atom.Feed) {
	body, err := atom.MarshalFeed(f)
	if err != nil {
		web.ServerError(w, err)
		return
	}
	sum := sha256.Sum256(body)
	etag := `"` + hex.EncodeToString(sum[:16]) + `"`
	w.Header().Set("ETag", etag)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", atom.MediaType+"; charset=utf-8")
	w.Write(body)
}

// WriteEntry writes a single entry as an Atom entry document.
func WriteEntry(w http.ResponseWriter, e *atom.Entry) {
	body, err := atom.MarshalEntry(e)
	if err != nil {
		web.ServerError(w, err)
		return
	}
	w.Header().Set("Content-Type", atom.EntryMediaType+"; charset=utf-8")
	w.Write(body)
}
