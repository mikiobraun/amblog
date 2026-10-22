package amagg

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"atomicmicro.blog/internal/web"
	"atomicmicro.blog/internal/webmention"
)

const ConfigFile = "amagg.toml"

type InitOptions struct {
	BaseURL string
	Name    string
	Listen  string
}

// Init sets up a new aggregator in dir: config and database. It refuses to
// overwrite an existing config.
func Init(dir string, opts InitOptions) (configPath string, err error) {
	configPath = filepath.Join(dir, ConfigFile)
	if _, err := os.Stat(configPath); err == nil {
		return "", fmt.Errorf("%s already exists", configPath)
	}
	cfg := &Config{
		Server: ServerConfig{Listen: opts.Listen},
		Aggregator: AggregatorConfig{
			Name:         opts.Name,
			BaseURL:      opts.BaseURL,
			Public:       true,
			Registration: "invite",
		},
		Polling: PollingConfig{
			DefaultInterval: Duration{5 * time.Minute},
			MinInterval:     Duration{time.Minute},
			MaxInterval:     Duration{time.Hour},
		},
		Storage:    StorageConfig{Path: "amagg.db"},
		WebMention: WebMentionConfig{PerHostPerHour: webmention.DefaultPerHostPerHour, BlockHosts: []string{}},
	}
	if err := cfg.validate(); err != nil {
		return "", err
	}
	store, err := OpenStore(filepath.Join(dir, cfg.Storage.Path))
	if err != nil {
		return "", err
	}
	if err := store.Close(); err != nil {
		return "", err
	}
	return configPath, WriteConfig(configPath, cfg)
}

// CreateInvite creates a single-use invite code and returns it.
func CreateInvite(store *Store) (string, error) {
	code, hash := web.NewToken("inv_")
	return code, store.CreateInvite(hash)
}
