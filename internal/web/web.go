// Package web holds the HTTP helpers shared by the servers: JSON in and out,
// CORS, bearer tokens.
package web

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"strings"
)

// CORS wraps h with permissive CORS headers and answers preflight requests.
// This is safe because authentication is always explicit (bearer tokens or
// HTTP Basic), never cookies, and Access-Control-Allow-Credentials is never sent.
func CORS(h http.Handler, methods string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hd := w.Header()
		hd.Set("Access-Control-Allow-Origin", "*")
		hd.Set("Access-Control-Expose-Headers", "Location, ETag")
		if r.Method == http.MethodOptions {
			hd.Set("Access-Control-Allow-Methods", methods)
			hd.Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// DecodeJSON decodes a JSON request body of at most 1 MiB, rejecting unknown
// fields. On failure it writes a 400 and returns false.
func DecodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return false
	}
	return true
}

func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func WriteError(w http.ResponseWriter, status int, msg string) {
	WriteJSON(w, status, map[string]string{"error": msg})
}

// ServerError logs err and writes a generic 500.
func ServerError(w http.ResponseWriter, err error) {
	log.Printf("internal error: %v", err)
	WriteError(w, http.StatusInternalServerError, "internal error")
}

// BearerToken returns the token from an "Authorization: Bearer" header.
func BearerToken(r *http.Request) (string, bool) {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return token, ok && token != ""
}

// NewToken returns a new random token with the given prefix (which tells
// tokens of different services apart) and the hash to store.
func NewToken(prefix string) (token, hash string) {
	token = prefix + RandomString(32)
	return token, HashToken(token)
}

// RandomString returns n random bytes, base64url-encoded.
func RandomString(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
