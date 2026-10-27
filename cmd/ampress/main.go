// Command ampress runs a blog for one feed.
//
//	ampress init -base-url URL -title TITLE -feed URL [-dir DIR]   write a config
//	ampress serve [-config FILE]                                   run the server and poller
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"atomicmicro.blog/internal/ampress"
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
		fmt.Fprintln(os.Stderr, "ampress:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: ampress init|serve [flags]")
	os.Exit(2)
}

func runInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	dir := fs.String("dir", ".", "directory for config and database")
	baseURL := fs.String("base-url", "", "public base URL of the blog, e.g. https://blog.miki.one (required)")
	title := fs.String("title", "", "blog title (required)")
	feed := fs.String("feed", "", "URL of the feed the blog shows (required)")
	listen := fs.String("listen", ":8400", "listen address")
	fs.Parse(args)
	if *baseURL == "" || *title == "" || *feed == "" {
		return errors.New("init: -base-url, -title and -feed are required")
	}
	if err := os.MkdirAll(*dir, 0o755); err != nil {
		return err
	}
	configPath, err := ampress.Init(*dir, ampress.InitOptions{BaseURL: *baseURL, Title: *title, Feed: *feed, Listen: *listen})
	if err != nil {
		return err
	}
	fmt.Printf("Created %s\n\n", configPath)
	fmt.Printf("Start the server:     ampress serve -config %s\n", configPath)
	fmt.Printf("Optional, in [blog]:  about = \"about.md\", theme = \"theme\"\n")
	return nil
}

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	configPath := fs.String("config", ampress.ConfigFile, "config file")
	fs.Parse(args)

	cfg, err := ampress.LoadConfig(*configPath)
	if err != nil {
		return err
	}
	store, err := ampress.OpenStore(cfg.Storage.Path)
	if err != nil {
		return err
	}
	defer store.Close()
	srv, err := ampress.NewServer(cfg, store)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	go srv.RunPoller(ctx)

	abs, _ := filepath.Abs(*configPath)
	log.Printf("ampress: serving %s on %s, showing %s (config %s)", cfg.Blog.BaseURL, cfg.Server.Listen, cfg.Blog.Feed, abs)
	hs := &http.Server{
		Addr:              cfg.Server.Listen,
		Handler:           srv,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
	}
	go func() {
		<-ctx.Done()
		hs.Shutdown(context.Background())
	}()
	if err := hs.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
