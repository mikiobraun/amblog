package amfeed

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"atomicmicro.blog/internal/sign"
	"atomicmicro.blog/internal/web"
	"atomicmicro.blog/internal/webmention"
)

const ConfigFile = "amfeed.toml"

type InitOptions struct {
	BaseURL    string
	AuthorName string
	Title      string
	Listen     string
}

// Init sets up a new feed server in dir: config, signing key, database, and
// an admin token, which is returned. It refuses to overwrite an existing config.
func Init(dir string, opts InitOptions) (configPath, adminToken string, err error) {
	configPath = filepath.Join(dir, ConfigFile)
	if _, err := os.Stat(configPath); err == nil {
		return "", "", fmt.Errorf("%s already exists", configPath)
	}
	now := time.Now().UTC().Truncate(time.Second)
	keyID := now.Format("2006-01")
	cfg := &Config{
		Server: ServerConfig{Listen: opts.Listen},
		Feed: FeedConfig{
			Title:      opts.Title,
			AuthorName: opts.AuthorName,
			AuthorURI:  opts.BaseURL + "/",
			BaseURL:    opts.BaseURL,
		},
		Signing: SigningConfig{
			PrivateKeyPath: filepath.Join("keys", keyID+".pem"),
			KeyID:          keyID,
			KeyCreated:     now,
		},
		Storage:    StorageConfig{Path: "amfeed.db"},
		WebMention: WebMentionConfig{PerHostPerHour: webmention.DefaultPerHostPerHour, BlockHosts: []string{}},
	}
	if err := cfg.validate(); err != nil {
		return "", "", err
	}

	if err := os.MkdirAll(filepath.Join(dir, "keys"), 0o700); err != nil {
		return "", "", err
	}
	key, err := sign.GenerateKey()
	if err != nil {
		return "", "", err
	}
	if err := sign.SavePrivateKey(filepath.Join(dir, cfg.Signing.PrivateKeyPath), key); err != nil {
		return "", "", err
	}

	store, err := OpenStore(filepath.Join(dir, cfg.Storage.Path))
	if err != nil {
		return "", "", err
	}
	defer store.Close()
	adminToken, hash := GenerateToken()
	if err := store.CreateToken("admin", hash, true, nil); err != nil {
		return "", "", err
	}

	if err := WriteConfig(configPath, cfg); err != nil {
		return "", "", err
	}
	return configPath, adminToken, nil
}

// GenerateToken returns a new random bearer token and the hash to store.
func GenerateToken() (token, hash string) { return web.NewToken("amf_") }
