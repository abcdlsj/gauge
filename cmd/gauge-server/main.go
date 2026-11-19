// Command gauge serves the dashboard over HTTP for headless or remote use.
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata" // guarantee America/Los_Angeles resolves in scratch images

	"github.com/abcdlsj/gauge/internal/app"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	addr := flag.String("addr", envOr("GAUGE_ADDR", "127.0.0.1:8090"), "listen address")
	dbPath := flag.String("db", envOr("GAUGE_DB", "quota.db"), "SQLite database path")
	keyPath := flag.String("key", envOr("GAUGE_KEY", ""), "encryption key file (default: next to the database)")
	flag.Parse()

	logger := log.New(os.Stdout, "", log.LstdFlags|log.Lmsgprefix)

	if *keyPath == "" {
		*keyPath = filepath.Join(filepath.Dir(*dbPath), "gauge.key")
	}

	authSpec := os.Getenv("GAUGE_AUTH")
	user, pass, _ := strings.Cut(authSpec, ":")
	if authSpec != "" && pass == "" {
		logger.Fatal("GAUGE_AUTH must be user:password")
	}
	if !isLoopback(*addr) && authSpec == "" && os.Getenv("GAUGE_ALLOW_PUBLIC") != "1" {
		logger.Fatal("refusing to listen on a public address without GAUGE_AUTH=user:password (set GAUGE_ALLOW_PUBLIC=1 to override)")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	a, err := app.New(ctx, app.Config{
		DBPath:   *dbPath,
		KeyPath:  *keyPath,
		Logger:   logger,
		Version:  version,
		AuthUser: user,
		AuthPass: pass,
	})
	if err != nil {
		logger.Fatalf("start: %v", err)
	}
	defer a.Close()

	a.Engine.Start(ctx)
	a.Updater.Start(ctx)

	srv := &http.Server{
		Addr:              *addr,
		Handler:           a.Handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	logger.Printf("gauge %s listening on http://%s (db %s)", version, *addr, *dbPath)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Fatal(err)
	}
	logger.Println("stopped")
}

// envOr returns the environment value or a fallback.
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// isLoopback reports whether the listen address only accepts local traffic.
func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	if host == "" || host == "localhost" {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}
