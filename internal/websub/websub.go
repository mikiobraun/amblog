// Package websub holds what the WebSub (W3C) hub in amfeed and the
// subscriber in amagg share: the parameters and the content signature. See
// the spec, "WebSub".
package websub

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"
)

const (
	// Path of amfeed's hub, under its base URL.
	HubPath = "/websub"

	DefaultLease = 10 * 24 * time.Hour
	MinLease     = time.Hour
	MaxLease     = 30 * 24 * time.Hour

	// MaxSecret is the longest hub.secret a hub accepts (WebSub: under 200 bytes).
	MaxSecret = 199
)

// Signature returns the X-Hub-Signature value for body.
func Signature(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// CheckSignature reports whether an X-Hub-Signature header matches body.
// Only sha256 is accepted.
func CheckSignature(secret, header string, body []byte) bool {
	if !strings.HasPrefix(header, "sha256=") {
		return false
	}
	return hmac.Equal([]byte(header), []byte(Signature(secret, body)))
}

// ClampLease limits a requested lease to [MinLease, MaxLease]; 0 means the default.
func ClampLease(d time.Duration) time.Duration {
	if d == 0 {
		return DefaultLease
	}
	return min(max(d, MinLease), MaxLease)
}
