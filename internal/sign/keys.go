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
	"time"
)

const WellKnownPath = "/.well-known/atomic-keys"

// KeyDoc is the JSON document served at /.well-known/atomic-keys.
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

// KeysURL returns the key document URL for an author URI.
func KeysURL(authorURI string) (string, error) {
	u, err := url.Parse(authorURI)
	if err != nil {
		return "", err
	}
	if !u.IsAbs() {
		return "", fmt.Errorf("sign: author uri %q is not absolute", authorURI)
	}
	return u.ResolveReference(&url.URL{Path: WellKnownPath}).String(), nil
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
