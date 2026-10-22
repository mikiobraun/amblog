// Command amagg runs an aggregator.
//
//	amagg init -base-url URL -name NAME [-dir DIR]   set up config and database
//	amagg serve [-config FILE]                       run the server and poller
//	amagg invite [-config FILE]                      create a single-use invite code
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

	"atomicmicro.blog/internal/amagg"
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
	case "invite":
		err = runInvite(os.Args[2:])
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "amagg:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: amagg init|serve|invite [flags]")
	os.Exit(2)
}

func runInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	dir := fs.String("dir", ".", "directory for config and database")
	baseURL := fs.String("base-url", "", "public base URL, e.g. https://capyclub.example (required)")
	name := fs.String("name", "", "aggregator name (required)")
	listen := fs.String("listen", ":8200", "listen address")
	fs.Parse(args)
	if *baseURL == "" || *name == "" {
		return errors.New("init: -base-url and -name are required")
	}
	if err := os.MkdirAll(*dir, 0o755); err != nil {
		return err
	}
	configPath, err := amagg.Init(*dir, amagg.InitOptions{BaseURL: *baseURL, Name: *name, Listen: *listen})
	if err != nil {
		return err
	}
	fmt.Printf("Created %s (registration: invite)\n\n", configPath)
	fmt.Printf("Start the server:     amagg serve -config %s\n", configPath)
	fmt.Printf("Create an invite:     amagg invite -config %s\n", configPath)
	return nil
}

func openStore(configPath string) (*amagg.Config, *amagg.Store, error) {
	cfg, err := amagg.LoadConfig(configPath)
	if err != nil {
		return nil, nil, err
	}
	store, err := amagg.OpenStore(cfg.Storage.Path)
	if err != nil {
		return nil, nil, err
	}
	return cfg, store, nil
}

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	configPath := fs.String("config", amagg.ConfigFile, "config file")
	fs.Parse(args)

	cfg, store, err := openStore(*configPath)
	if err != nil {
		return err
	}
	defer store.Close()
	srv, err := amagg.NewServer(cfg, store)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	go srv.RunPoller(ctx)

	abs, _ := filepath.Abs(*configPath)
	log.Printf("amagg: serving %s on %s (config %s)", cfg.Aggregator.BaseURL, cfg.Server.Listen, abs)
	hs := &http.Server{
		Addr:              cfg.Server.Listen,
		Handler:           srv,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		// Login verification and follows fetch remote URLs while the client waits.
		WriteTimeout: 90 * time.Second,
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

func runInvite(args []string) error {
	fs := flag.NewFlagSet("invite", flag.ExitOnError)
	configPath := fs.String("config", amagg.ConfigFile, "config file")
	fs.Parse(args)
	_, store, err := openStore(*configPath)
	if err != nil {
		return err
	}
	defer store.Close()
	code, err := amagg.CreateInvite(store)
	if err != nil {
		return err
	}
	fmt.Println(code)
	return nil
}
