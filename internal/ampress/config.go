package ampress

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"

	"atomicmicro.blog/internal/webmention"
)

const ConfigFile = "ampress.toml"

type Config struct {
	Server     ServerConfig     `toml:"server"`
	Blog       BlogConfig       `toml:"blog"`
	WebMention WebMentionConfig `toml:"webmention"`
	Storage    StorageConfig    `toml:"storage"`
}

type ServerConfig struct {
	Listen string `toml:"listen"`
}

type BlogConfig struct {
	Title   string `toml:"title"`
	BaseURL string `toml:"base_url"`
	Feed    string `toml:"feed"`            // the one feed this blog shows
	About   string `toml:"about,omitempty"` // optional: Markdown file, rendered as /about
	Theme   string `toml:"theme,omitempty"` // optional: folder overriding the built-in theme
}

// WebMentionConfig is spam control for incoming WebMentions. The section is
// optional; without it, the defaults apply.
type WebMentionConfig struct {
	PerHostPerHour int      `toml:"per_host_per_hour"`
	BlockHosts     []string `toml:"block_hosts"`
}

type StorageConfig struct {
	Path string `toml:"path"`
}

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
	if !md.IsDefined("webmention", "per_host_per_hour") {
		c.WebMention.PerHostPerHour = webmention.DefaultPerHostPerHour
	}
	if err := c.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	dir := filepath.Dir(path)
	for _, p := range []*string{&c.Storage.Path, &c.Blog.About, &c.Blog.Theme} {
		if *p != "" && !filepath.IsAbs(*p) {
			*p = filepath.Join(dir, *p)
		}
	}
	if c.Blog.About != "" {
		if _, err := os.Stat(c.Blog.About); err != nil {
			return nil, fmt.Errorf("%s: blog.about: %w", path, err)
		}
	}
	if c.Blog.Theme != "" {
		if fi, err := os.Stat(c.Blog.Theme); err != nil || !fi.IsDir() {
			return nil, fmt.Errorf("%s: blog.theme: %s is not a directory", path, c.Blog.Theme)
		}
	}
	return &c, nil
}

type InitOptions struct {
	BaseURL string
	Title   string
	Feed    string
	Listen  string
}

// Init writes a config file into dir and returns its path. The database is
// created on the first start.
func Init(dir string, opts InitOptions) (string, error) {
	c := &Config{
		Server:     ServerConfig{Listen: opts.Listen},
		Blog:       BlogConfig{Title: opts.Title, BaseURL: strings.TrimSuffix(opts.BaseURL, "/"), Feed: opts.Feed},
		WebMention: WebMentionConfig{PerHostPerHour: webmention.DefaultPerHostPerHour, BlockHosts: []string{}},
		Storage:    StorageConfig{Path: "ampress.db"},
	}
	if err := c.validate(); err != nil {
		return "", err
	}
	path := filepath.Join(dir, ConfigFile)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return "", err
	}
	if err := toml.NewEncoder(f).Encode(c); err != nil {
		f.Close()
		return "", err
	}
	return path, f.Close()
}

func (c *Config) validate() error {
	var errs []error
	require := func(name, value string) {
		if value == "" {
			errs = append(errs, fmt.Errorf("%s is required", name))
		}
	}
	require("server.listen", c.Server.Listen)
	require("blog.title", c.Blog.Title)
	require("storage.path", c.Storage.Path)
	u, err := url.Parse(c.Blog.BaseURL)
	switch {
	case err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "":
		errs = append(errs, fmt.Errorf("blog.base_url: %q is not an absolute http(s) URL", c.Blog.BaseURL))
	case strings.HasSuffix(c.Blog.BaseURL, "/"):
		errs = append(errs, errors.New("blog.base_url must not end with /"))
	case u.RawQuery != "" || u.Fragment != "":
		errs = append(errs, errors.New("blog.base_url must not have a query or fragment"))
	}
	if u, err := url.Parse(c.Blog.Feed); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		errs = append(errs, fmt.Errorf("blog.feed: %q is not an absolute http(s) URL", c.Blog.Feed))
	}
	if c.WebMention.PerHostPerHour < 1 {
		errs = append(errs, errors.New("webmention.per_host_per_hour must be at least 1"))
	}
	return errors.Join(errs...)
}
