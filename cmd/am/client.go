package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"atomicmicro.blog/internal/atom"
	"atomicmicro.blog/internal/sign"
	"github.com/BurntSushi/toml"
)

// Config is the CLI config, stored in $AM_CONFIG or <user config dir>/am/config.toml.
type Config struct {
	FeedURL string `toml:"feed_url"`
	Token   string `toml:"token"`
}

func configPath() (string, error) {
	if p := os.Getenv("AM_CONFIG"); p != "" {
		return p, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "am", "config.toml"), nil
}

// loadConfig returns an empty config if the file doesn't exist yet.
func loadConfig() (*Config, string, error) {
	path, err := configPath()
	if err != nil {
		return nil, "", err
	}
	var c Config
	md, err := toml.DecodeFile(path, &c)
	if errors.Is(err, os.ErrNotExist) {
		return &c, path, nil
	}
	if err != nil {
		return nil, "", err
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		return nil, "", fmt.Errorf("%s: unknown keys %v", path, undecoded)
	}
	return &c, path, nil
}

func saveConfig(path string, c *Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(c); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o600)
}

var httpClient = &http.Client{Timeout: 30 * time.Second}

// feedClient talks to the user's own feed server API.
type feedClient struct {
	cfg *Config
}

func newFeedClient() (*feedClient, error) {
	cfg, path, err := loadConfig()
	if err != nil {
		return nil, err
	}
	if cfg.FeedURL == "" || cfg.Token == "" {
		return nil, fmt.Errorf("no feed server configured in %s; run: am config -feed URL -token TOKEN", path)
	}
	return &feedClient{cfg: cfg}, nil
}

// do sends a JSON request and decodes a JSON response into out (if non-nil).
func (c *feedClient) do(method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, c.cfg.FeedURL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		var e struct{ Error string }
		if json.Unmarshal(data, &e) == nil && e.Error != "" {
			return fmt.Errorf("%s %s: %s: %s", method, path, resp.Status, e.Error)
		}
		return fmt.Errorf("%s %s: %s", method, path, resp.Status)
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

// fetchAtom fetches a feed or entry document from any URL.
func fetchAtom(url string) (*atom.Feed, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", atom.MediaType)
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	f, err := atom.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", url, err)
	}
	return f, nil
}

// keyCache fetches and remembers key documents per author URI.
type keyCache map[string]*sign.KeyDoc

func (kc keyCache) get(authorURI string) (*sign.KeyDoc, error) {
	if doc, ok := kc[authorURI]; ok {
		return doc, nil
	}
	url, err := sign.KeysURL(authorURI)
	if err != nil {
		return nil, err
	}
	resp, err := httpClient.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	var doc sign.KeyDoc
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return nil, fmt.Errorf("GET %s: %w", url, err)
	}
	kc[authorURI] = &doc
	return &doc, nil
}

func (kc keyCache) verify(e *atom.Entry) error {
	if e.Author == nil || e.Author.URI == "" {
		return errors.New("entry has no author uri")
	}
	keys, err := kc.get(e.Author.URI)
	if err != nil {
		return err
	}
	return sign.Verify(e, keys)
}
