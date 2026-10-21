package amfeed

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

type Config struct {
	Server  ServerConfig  `toml:"server"`
	Feed    FeedConfig    `toml:"feed"`
	Signing SigningConfig `toml:"signing"`
	Storage StorageConfig `toml:"storage"`
}

type ServerConfig struct {
	Listen string `toml:"listen"`
}

type FeedConfig struct {
	Title      string `toml:"title"`
	AuthorName string `toml:"author_name"`
	AuthorURI  string `toml:"author_uri"`
	BaseURL    string `toml:"base_url"`
}

type SigningConfig struct {
	PrivateKeyPath string    `toml:"private_key_path"`
	KeyID          string    `toml:"key_id"`
	KeyCreated     time.Time `toml:"key_created"`
}

type StorageConfig struct {
	Path string `toml:"path"`
}

// LoadConfig reads a config file. Relative paths in it are resolved against
// the directory containing the config file. Unknown keys are an error.
func LoadConfig(path string) (*Config, error) {
	var c Config
	md, err := toml.DecodeFile(path, &c)
	if err != nil {
		return nil, err
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		return nil, fmt.Errorf("%s: unknown keys %v", path, undecoded)
	}
	if err := c.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	dir := filepath.Dir(path)
	c.Signing.PrivateKeyPath = resolvePath(dir, c.Signing.PrivateKeyPath)
	c.Storage.Path = resolvePath(dir, c.Storage.Path)
	return &c, nil
}

func WriteConfig(path string, c *Config) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if err := toml.NewEncoder(f).Encode(c); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func (c *Config) validate() error {
	var errs []error
	require := func(name, value string) {
		if value == "" {
			errs = append(errs, fmt.Errorf("%s is required", name))
		}
	}
	require("server.listen", c.Server.Listen)
	require("feed.title", c.Feed.Title)
	require("feed.author_name", c.Feed.AuthorName)
	require("signing.private_key_path", c.Signing.PrivateKeyPath)
	require("signing.key_id", c.Signing.KeyID)
	require("storage.path", c.Storage.Path)
	if err := checkAbsURL(c.Feed.BaseURL); err != nil {
		errs = append(errs, fmt.Errorf("feed.base_url: %w", err))
	} else if strings.HasSuffix(c.Feed.BaseURL, "/") {
		errs = append(errs, errors.New("feed.base_url must not end with /"))
	}
	if err := checkAbsURL(c.Feed.AuthorURI); err != nil {
		errs = append(errs, fmt.Errorf("feed.author_uri: %w", err))
	}
	return errors.Join(errs...)
}

func checkAbsURL(s string) error {
	u, err := url.Parse(s)
	if err != nil {
		return err
	}
	if u.Scheme != "http" && u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("%q is not an absolute http(s) URL", s)
	}
	return nil
}

func resolvePath(dir, p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(dir, p)
}
