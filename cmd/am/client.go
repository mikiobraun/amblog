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
	"github.com/BurntSushi/toml"
)

// Config is the CLI config, stored in $AM_CONFIG or <user config dir>/am/config.toml.
type Config struct {
	FeedURL string `toml:"feed_url"`
	Token   string `toml:"token"`

	// Set by "am login".
	AggURL      string `toml:"agg_url,omitempty"`
	AggToken    string `toml:"agg_token,omitempty"`
	TimelineURL string `toml:"timeline_url,omitempty"`
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
	return doJSON(method, c.cfg.FeedURL+path, c.cfg.Token, in, out)
}

// doJSON sends a request with an optional JSON body and bearer token, and
// decodes a JSON response into out (if non-nil).
func doJSON(method, url, token string, in, out any) error {
	var body io.Reader
	contentType := ""
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body, contentType = bytes.NewReader(data), "application/json"
	}
	data, err := doRequest(method, url, token, contentType, body)
	if err != nil || out == nil {
		return err
	}
	return json.Unmarshal(data, out)
}

// doRequest sends a request and returns the response body; statuses >= 300
// are errors, with the server's error message if it sent one.
func doRequest(method, url, token, contentType string, body io.Reader) ([]byte, error) {
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		var e struct{ Error string }
		if json.Unmarshal(data, &e) == nil && e.Error != "" {
			return nil, fmt.Errorf("%s %s: %s: %s", method, url, resp.Status, e.Error)
		}
		return nil, fmt.Errorf("%s %s: %s", method, url, resp.Status)
	}
	return data, nil
}

// fetchAtom fetches a feed or entry document from any URL.
func fetchAtom(url string) (*atom.Feed, error) { return fetchAtomWith(url, "") }

// fetchAtomWith fetches a feed or entry document, with a bearer token if
// token is not empty.
func fetchAtomWith(url, token string) (*atom.Feed, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", atom.MediaType)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
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
