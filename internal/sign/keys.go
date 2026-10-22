package sign

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

// Paths relative to the author URI (see IdentityURL).
const (
	KeysPath      = "atomic-keys"      // the key document
	ChallengePath = "atomic-challenge" // login challenges: <ChallengePath>/<token>
)

// KeyDoc is the JSON document served at <author URI>/atomic-keys.
type KeyDoc struct {
	Keys []Key `json:"keys"`
}

type Key struct {
	ID        string     `json:"id"`
	Algorithm string     `json:"algorithm"`
	PublicKey string     `json:"public_key"`
	Created   time.Time  `json:"created"`
	Expires   *time.Time `json:"expires"`
}

func NewKey(id string, pub ed25519.PublicKey, created time.Time) Key {
	return Key{
		ID:        id,
		Algorithm: Algorithm,
		PublicKey: base64.StdEncoding.EncodeToString(pub),
		Created:   created,
	}
}

func (d *KeyDoc) Find(id string) (*Key, bool) {
	for i := range d.Keys {
		if d.Keys[i].ID == id {
			return &d.Keys[i], true
		}
	}
	return nil, false
}

func (k *Key) Ed25519() (ed25519.PublicKey, error) {
	if k.Algorithm != Algorithm {
		return nil, fmt.Errorf("sign: key %q has unsupported algorithm %q", k.ID, k.Algorithm)
	}
	b, err := base64.StdEncoding.DecodeString(k.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("sign: key %q: %w", k.ID, err)
	}
	if len(b) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("sign: key %q has length %d, want %d", k.ID, len(b), ed25519.PublicKeySize)
	}
	return ed25519.PublicKey(b), nil
}

// KeysURL returns the key document URL for an author URI: KeysPath appended
// to the URI's path, so identities with a path (https://host.example/alice)
// get their own key document.
func KeysURL(authorURI string) (string, error) { return IdentityURL(authorURI, KeysPath) }

// IdentityURL returns the URL of rel (a relative path like "atomic-keys")
// under an identity: rel appended to the author URI's path, ignoring a
// trailing slash. Author URIs with a query or fragment are rejected.
func IdentityURL(authorURI, rel string) (string, error) {
	u, err := url.Parse(authorURI)
	if err != nil {
		return "", err
	}
	if !u.IsAbs() || u.Host == "" {
		return "", fmt.Errorf("sign: author uri %q is not absolute", authorURI)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("sign: author uri %q has a query or fragment", authorURI)
	}
	u.Path = strings.TrimSuffix(u.Path, "/") + "/" + rel
	u.RawPath = ""
	return u.String(), nil
}

func GenerateKey() (ed25519.PrivateKey, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	return priv, err
}

// SavePrivateKey writes the key as PKCS#8 PEM, readable only by the owner.
func SavePrivateKey(path string, key ed25519.PrivateKey) error {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return err
	}
	data := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	return os.WriteFile(path, data, 0o600)
}

func LoadPrivateKey(path string) (ed25519.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "PRIVATE KEY" {
		return nil, fmt.Errorf("sign: %s: no PRIVATE KEY PEM block", path)
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("sign: %s: %w", path, err)
	}
	priv, ok := key.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("sign: %s: not an ed25519 key", path)
	}
	return priv, nil
}
