package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/GoldenDeo/filekeeper/internal/blob"
	"github.com/GoldenDeo/filekeeper/internal/config"
	"github.com/GoldenDeo/filekeeper/internal/meta"
	"github.com/GoldenDeo/filekeeper/internal/server"
)

func main() {
	if err := run(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "filekeeper:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	spoolDir := filepath.Join(cfg.DataDir, "tmp")
	if err := os.MkdirAll(spoolDir, 0o755); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}

	metaStore, err := meta.Open(filepath.Join(cfg.DataDir, "filekeeper.db"))
	if err != nil {
		return err
	}
	defer metaStore.Close()

	blobs, err := blob.NewS3(ctx, cfg.S3)
	if err != nil {
		return err
	}

	tokens := make(map[string]server.Token, len(cfg.Tokens))
	for token, t := range cfg.Tokens {
		tokens[token] = server.Token{Service: t.Service, Scope: server.Scope(t.Scope)}
	}

	handler := server.New(server.Config{
		PublicBaseURL:  cfg.PublicBaseURL,
		SpoolDir:       spoolDir,
		MaxUploadBytes: cfg.MaxUploadBytes,
		AllowedMIME:    cfg.AllowedMIME,
		Tokens:         tokens,
		SignSecret:     cfg.SignSecret,
		CORSOrigins:    cfg.CORSOrigins,
	}, metaStore, blobs, logger)

	srv := &http.Server{
		Addr:    cfg.Addr,
		Handler: handler,
		// No ReadTimeout: it is an absolute deadline over the whole body and
		// would kill legitimate slow uploads. Slowloris is bounded by
		// ReadHeaderTimeout; body size is bounded by MaxBytesReader.
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", cfg.Addr, "public_url", cfg.PublicBaseURL)
		if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	// Let in-flight uploads finish; k8s/docker must grant a grace period
	// larger than this.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	logger.Info("shutting down")
	return srv.Shutdown(shutdownCtx)
}
