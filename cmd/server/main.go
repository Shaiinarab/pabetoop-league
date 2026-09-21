// Command server is the youth football platform's HTTP entrypoint:
// config → store (open + migrate) → web.Server → graceful shutdown.
//
// Run:
//
//	ADMIN_PASSWORD=… go run ./cmd/server           # dev
//	./server -addr :8080 -db data/pabetoop-league.db # prod-ish
//
// Environment (all optional except ADMIN_PASSWORD in production):
//
//	ADDR             listen address (default :8080, flag wins)
//	DB_PATH          SQLite file      (default data/pabetoop-league.db, flag wins)
//	SESSION_SECRET   HMAC secret; else data/secret.key is created (0600)
//	ADMIN_PASSWORD   bootstrap admin password (bcrypt-hashed at startup)
//	ADMIN_PASSWORD_HASH  pre-hashed bcrypt variant (wins over ADMIN_PASSWORD)
//	ADMIN_USER       defaults to "admin"
//	TEMPLATES_DIR    default web/templates · STATIC_DIR default web/static
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/shaiinarab/pabetoop-league/internal/store"
	"github.com/shaiinarab/pabetoop-league/internal/web"
)

func main() {
	addr := flag.String("addr", envOr("ADDR", ":8080"), "HTTP listen address")
	dbPath := flag.String("db", envOr("DB_PATH", "data/pabetoop-league.db"), "SQLite database file")
	flag.Parse()

	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	st, err := store.Open(*dbPath)
	if err != nil {
		log.Error("open store", "path", *dbPath, "error", err)
		os.Exit(1)
	}
	if err := st.Migrate(); err != nil {
		log.Error("migrate store", "error", err)
		_ = st.Close()
		os.Exit(1)
	}

	srv := web.New(st)
	if err := srv.TemplateErr(); err != nil {
		// Fail fast: a broken template set must never reach production.
		log.Error("parse templates", "error", err)
		_ = st.Close()
		os.Exit(1)
	}

	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		log.Info("server listening", "addr", *addr, "db", *dbPath)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		log.Error("server failed", "error", err)
		_ = st.Close()
		os.Exit(1)
	case <-ctx.Done():
		log.Info("shutdown signal received — draining")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpSrv.Shutdown(shutdownCtx); err != nil {
			log.Error("graceful shutdown failed", "error", err)
		}
		if err := st.Close(); err != nil {
			log.Error("close store", "error", err)
		}
		log.Info("shutdown complete")
	}
}

// envOr returns the env value or a fallback (kept local so flags can show defaults).
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
