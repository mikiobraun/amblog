// Command amfeed runs a feed server.
//
//	amfeed init -base-url URL -name NAME [-dir DIR]   set up config, key, database
//	amfeed serve [-config FILE]                       run the server
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"atomicmicro.blog/internal/amfeed"
	"atomicmicro.blog/internal/sign"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "init":
		err = runInit(os.Args[2:])
	case "serve":
		err = runServe(os.Args[2:])
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "amfeed:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: amfeed init|serve [flags]")
	os.Exit(2)
}

func runInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	dir := fs.String("dir", ".", "directory for config, keys and database")
	baseURL := fs.String("base-url", "", "public base URL, e.g. https://miki.one (required)")
	name := fs.String("name", "", "author name (required)")
	title := fs.String("title", "", "feed title (default: \"<name>'s Feed\")")
	listen := fs.String("listen", ":8100", "listen address")
	fs.Parse(args)
	if *baseURL == "" || *name == "" {
		return errors.New("init: -base-url and -name are required")
	}
	if *title == "" {
		*title = *name + "'s Feed"
	}
	if err := os.MkdirAll(*dir, 0o755); err != nil {
		return err
	}
	configPath, token, err := amfeed.Init(*dir, amfeed.InitOptions{
		BaseURL: *baseURL, AuthorName: *name, Title: *title, Listen: *listen,
	})
	if err != nil {
		return err
	}
	fmt.Printf("Created %s\n\nAdmin token (shown only once):\n\n  %s\n\n", configPath, token)
	fmt.Printf("Start the server:  amfeed serve -config %s\n", configPath)
	fmt.Printf("Configure the CLI: am config -feed %s -token %s\n", *baseURL, token)
	return nil
}

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	configPath := fs.String("config", amfeed.ConfigFile, "config file")
	fs.Parse(args)

	cfg, err := amfeed.LoadConfig(*configPath)
	if err != nil {
		return err
	}
	store, err := amfeed.OpenStore(cfg.Storage.Path)
	if err != nil {
		return err
	}
	defer store.Close()
	key, err := sign.LoadPrivateKey(cfg.Signing.PrivateKeyPath)
	if err != nil {
		return err
	}
	srv, err := amfeed.NewServer(cfg, store, key)
	if err != nil {
		return err
	}
	abs, _ := filepath.Abs(*configPath)
	log.Printf("amfeed: serving %s on %s (config %s)", cfg.Feed.BaseURL, cfg.Server.Listen, abs)
	if cfg.Content != nil {
		log.Printf("amfeed: publishing %s from %s", cfg.Content.Pattern, cfg.Content.Dir)
		go srv.RunContentSync(context.Background())
	}
	hs := &http.Server{
		Addr:              cfg.Server.Listen,
		Handler:           srv,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
	}
	return hs.ListenAndServe()
}
