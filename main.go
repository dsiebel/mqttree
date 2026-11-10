// Command mqttree is a self-hosted MQTT topic browser: it subscribes to a
// broker, keeps the topic tree and message history in SQLite, and serves a
// mobile friendly UI plus a JSON API.
package main

import (
	"context"
	"embed"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/dsiebel/mqttree/internal/api"
	"github.com/dsiebel/mqttree/internal/broker"
	"github.com/dsiebel/mqttree/internal/config"
	"github.com/dsiebel/mqttree/internal/store"
)

//go:embed all:web
var webAssets embed.FS

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

const pruneInterval = 5 * time.Minute

func main() {
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Printf("mqttree %s (%s, %s)\n", version, commit, date)
		return
	}
	if err := run(); err != nil {
		slog.Error("startup failed", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}
	level := slog.LevelInfo
	switch cfg.LogLevel {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))
	slog.Info("starting mqttree", "version", version, "listen", cfg.Listen, "data_dir", cfg.DataDir)
	if !isLoopback(cfg.Listen) {
		slog.Warn("listening on a non-loopback address; mqttree has no authentication, put an authenticating reverse proxy in front of it or firewall the port",
			"listen", cfg.Listen)
	}

	key, err := store.LoadKey(cfg.SecretKeyFile)
	if err != nil {
		return err
	}
	st, err := store.Open(cfg.DBPath(), key)
	if err != nil {
		return err
	}
	defer st.Close()

	assets, err := fs.Sub(webAssets, "web")
	if err != nil {
		return fmt.Errorf("open embedded assets: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	hub := api.NewHub()
	go hub.Run(ctx)

	manager := broker.New(st, hub.Publish)
	manager.StartAll(ctx)
	defer manager.Shutdown()

	go pruneLoop(ctx, st)

	server := &http.Server{
		Addr:              cfg.Listen,
		Handler:           api.New(st, manager, hub, assets).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}

	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.Listen, err)
	}
	slog.Info("listening", "address", listener.Addr().String())

	errs := make(chan error, 1)
	go func() {
		errs <- server.Serve(listener)
	}()

	select {
	case err := <-errs:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serve: %w", err)
		}
	case <-ctx.Done():
		slog.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown: %w", err)
		}
	}
	return nil
}

// isLoopback reports whether the listen address is restricted to the local
// host, which is the only exposure the binary can safely assume.
func isLoopback(listen string) bool {
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return false
	}
	if host == "" {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func pruneLoop(ctx context.Context, st *store.Store) {
	prune := func() {
		deleted, err := st.Prune(ctx, time.Now().UnixMilli())
		if err != nil {
			slog.Error("prune", "error", err)
			return
		}
		if deleted > 0 {
			slog.Info("pruned messages", "count", deleted)
		}
	}
	prune()
	ticker := time.NewTicker(pruneInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			prune()
		}
	}
}
