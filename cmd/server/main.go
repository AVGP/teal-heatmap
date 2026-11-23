package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"teal-heatmap/internal/api"
	"teal-heatmap/internal/auth"
	"teal-heatmap/internal/config"
	"teal-heatmap/internal/handlers"
	"teal-heatmap/internal/store"
	"teal-heatmap/internal/visualization"
)

func main() {
	storageFlag := flag.String("storage", "", "datastore backend: postgres or memory (overrides STORAGE env var)")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	if err := run(*storageFlag); err != nil {
		slog.Error("server exited with error", "err", err)
		os.Exit(1)
	}
}

func run(storageFlag string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if s := strings.ToLower(strings.TrimSpace(storageFlag)); s != "" {
		if s != "postgres" && s != "memory" {
			return fmt.Errorf("invalid -storage %q: must be \"postgres\" or \"memory\"", storageFlag)
		}
		cfg.Storage = s
	}

	var dataStore store.Store
	switch cfg.Storage {
	case "memory":
		slog.Warn("using in-memory datastore (sessions and cached plays will reset on restart)")
		dataStore = store.NewMemoryStore()
	default:
		pgStore, err := store.NewPostgresStore(ctx, cfg.DatabaseURL)
		if err != nil {
			return err
		}
		dataStore = pgStore
	}
	defer dataStore.Close()

	// Periodically garbage-collect expired browser sessions and stale OAuth state.
	go func() {
		ticker := time.NewTicker(30 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				cleanCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
				if err := dataStore.CleanupExpired(cleanCtx); err != nil {
					slog.Warn("cleanup expired sessions failed", "err", err)
				}
				cancel()
			}
		}
	}()

	renderer, err := visualization.NewRenderer()
	if err != nil {
		return err
	}

	oauthClient := auth.NewClient(cfg, dataStore)
	tealClient := api.NewTealClient()
	srv := handlers.NewServer(cfg, dataStore, oauthClient, tealClient, renderer)

	httpServer := &http.Server{
		Addr:              cfg.Addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       90 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("starting teal.fm heatmap server",
			"addr", cfg.Addr,
			"base_url", cfg.BaseURL,
			"oauth_loopback", cfg.IsLoopback,
			"storage", cfg.Storage,
		)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		slog.Info("shutting down server")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return httpServer.Shutdown(shutdownCtx)
	}
}
