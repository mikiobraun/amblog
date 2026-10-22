package amagg

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"atomicmicro.blog/internal/webmention"
)

type Config struct {
	Server     ServerConfig     `toml:"server"`
	Aggregator AggregatorConfig `toml:"aggregator"`
	Polling    PollingConfig    `toml:"polling"`
	Storage    StorageConfig    `toml:"storage"`
	WebMention WebMentionConfig `toml:"webmention"`
}

// WebMentionConfig is spam control for incoming WebMentions. The section is
// optional; without it, the defaults apply.
type WebMentionConfig struct {
	PerHostPerHour int      `toml:"per_host_per_hour"` // then 429
	BlockHosts     []string `toml:"block_hosts"`       // hosts (and their subdomains) whose mentions are rejected
}

type ServerConfig struct {
	Listen string `toml:"listen"`
}

type AggregatorConfig struct {
	Name    string `toml:"name"`
	BaseURL string `toml:"base_url"`
	// Public: island timeline, post pages and threads are readable without login.
	Public bool `toml:"public"`
	// Registration is "invite" or "open".
	Registration string `toml:"registration"`
}

type PollingConfig struct {
	DefaultInterval Duration `toml:"default_interval"`
	MinInterval     Duration `toml:"min_interval"`
	MaxInterval     Duration `toml:"max_interval"`
}

type StorageConfig struct {
	Path string `toml:"path"`
}

// Duration is a time.Duration written as a string like "5m" in TOML.
type Duration struct{ time.Duration }

func (d *Duration) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(string(b))
	d.Duration = v
	return err
}

func (d Duration) MarshalText() ([]byte, error) { return []byte(d.String()), nil }

// LoadConfig reads a config file. Relative paths in it are resolved against
// the directory containing the config file. Unknown and missing keys are errors.
func LoadConfig(path string) (*Config, error) {
	var c Config
	md, err := toml.DecodeFile(path, &c)
	if err != nil {
		return nil, err
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		return nil, fmt.Errorf("%s: unknown keys %v", path, undecoded)
	}
	// A missing boolean would silently mean false, i.e. a private island.
	if !md.IsDefined("aggregator", "public") {
		return nil, fmt.Errorf("%s: aggregator.public is required", path)
	}
	if !md.IsDefined("webmention", "per_host_per_hour") {
		c.WebMention.PerHostPerHour = webmention.DefaultPerHostPerHour
	}
	if err := c.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if !filepath.IsAbs(c.Storage.Path) {
		c.Storage.Path = filepath.Join(filepath.Dir(path), c.Storage.Path)
	}
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
	require("aggregator.name", c.Aggregator.Name)
	require("storage.path", c.Storage.Path)
	u, err := url.Parse(c.Aggregator.BaseURL)
	switch {
	case err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "":
		errs = append(errs, fmt.Errorf("aggregator.base_url: %q is not an absolute http(s) URL", c.Aggregator.BaseURL))
	case strings.HasSuffix(c.Aggregator.BaseURL, "/"):
		errs = append(errs, errors.New("aggregator.base_url must not end with /"))
	case u.RawQuery != "" || u.Fragment != "":
		errs = append(errs, errors.New("aggregator.base_url must not have a query or fragment"))
	}
	if r := c.Aggregator.Registration; r != "invite" && r != "open" {
		errs = append(errs, fmt.Errorf(`aggregator.registration must be "invite" or "open", not %q`, r))
	}
	p := c.Polling
	if p.MinInterval.Duration <= 0 || p.MinInterval.Duration > p.DefaultInterval.Duration || p.DefaultInterval.Duration > p.MaxInterval.Duration {
		errs = append(errs, errors.New("polling: need 0 < min_interval <= default_interval <= max_interval"))
	}
	if c.WebMention.PerHostPerHour < 1 {
		errs = append(errs, errors.New("webmention.per_host_per_hour must be at least 1"))
	}
	return errors.Join(errs...)
}
