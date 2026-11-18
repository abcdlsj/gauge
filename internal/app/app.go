// Package app wires the store, providers and HTTP surface together so the same
// dashboard can run as a CLI server or inside a desktop shell.
package app

import (
	"context"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/abcdlsj/gauge/internal/engine"
	"github.com/abcdlsj/gauge/internal/providers"
	"github.com/abcdlsj/gauge/internal/store"
	"github.com/abcdlsj/gauge/web"
)

// Config describes one instance of the dashboard.
type Config struct {
	DBPath   string
	KeyPath  string
	Logger   *log.Logger
	AuthUser string
	AuthPass string
	// AllowEmbedding relaxes frame protection for the desktop shell, which
	// renders the panel inside the app window.
	AllowEmbedding bool
	// OnSettingsChange, when set, runs after settings are saved or cleared.
	// changed maps each key to its new value; "" means it was cleared and is
	// back to its default.
	OnSettingsChange func(changed map[string]string)
}

// App owns the long-lived pieces. Close it when the process exits.
type App struct {
	Store   *store.Store
	Engine  *engine.Engine
	Handler http.Handler

	log *log.Logger
}

// New opens the database and builds the HTTP handler. The caller decides when
// to start the engine (desktop shells start it after the window is ready).
func New(ctx context.Context, cfg Config) (*App, error) {
	key, err := store.LoadKey(cfg.KeyPath)
	if err != nil {
		return nil, err
	}
	st, err := store.Open(cfg.DBPath, key)
	if err != nil {
		return nil, err
	}
	if err := st.InitSchema(ctx); err != nil {
		st.Close()
		return nil, err
	}

	deps := providers.NewDeps(st, cfg.Logger)
	deps.LoadCachedAccounts(ctx)
	eng := engine.New(st, deps, []providers.Provider{
		providers.Antigravity{},
		providers.GCPBilling{},
		providers.GCPMonitoring{},
		providers.Colab{},
		providers.GoogleFlow{},
		providers.CommandCode{},
		providers.OpenDesign{},
	}, cfg.Logger)

	s := &Server{store: st, engine: eng, log: cfg.Logger, start: time.Now(), onChange: cfg.OnSettingsChange}
	broker := newOAuthBroker(st, eng, cfg.Logger)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/overview", s.handleOverview)
	mux.HandleFunc("GET /api/quotas", func(w http.ResponseWriter, r *http.Request) {
		ov, err := eng.Overview(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, ov.Quotas)
	})
	mux.HandleFunc("GET /api/settings", s.handleSettings)
	mux.HandleFunc("POST /api/settings", s.handleSettings)
	mux.HandleFunc("DELETE /api/settings/{key}", s.handleDeleteSetting)
	mux.HandleFunc("PUT /api/display", s.handleDisplay)
	mux.HandleFunc("POST /api/refresh-all", s.handleRefreshAll)
	mux.HandleFunc("POST /api/refresh/{provider}", s.handleRefresh)
	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("GET /api/avatar/{slot}", s.handleAvatar)
	mux.HandleFunc("GET /api/oauth/start", broker.handleStart)
	mux.HandleFunc("DELETE /api/accounts/{slot}", s.handleDeleteAccount)
	mux.HandleFunc("GET /oauth/callback", broker.handleCallback)
	// The desktop shell redirects to the path Antigravity's own client
	// whitelists (/oauth-callback) because Google matches redirect URIs
	// exactly. The web build keeps the documented /oauth/callback.
	mux.HandleFunc("GET /oauth-callback", broker.handleCallback)
	mux.HandleFunc("GET /mark.svg", s.handleMark)
	mux.HandleFunc("GET /popover", func(w http.ResponseWriter, r *http.Request) {
		raw, err := web.FS.ReadFile("popover.html")
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(raw)
	})
	mux.HandleFunc("GET /desktop", func(w http.ResponseWriter, r *http.Request) {
		raw, err := web.FS.ReadFile("shell.html")
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(raw)
	})
	mux.Handle("GET /", noCache(http.FileServer(http.FS(web.FS))))

	handler := requireBasicAuth(securityHeaders(logRequests(mux, cfg.Logger), cfg.AllowEmbedding), cfg.AuthUser, cfg.AuthPass)

	return &App{Store: st, Engine: eng, Handler: handler, log: cfg.Logger}, nil
}

// Close releases the database.
func (a *App) Close() error { return a.Store.Close() }

// securityHeaders adds conservative defaults; the panel loads no third-party
// resources, so the CSP can stay strict.
func securityHeaders(next http.Handler, allowEmbedding bool) http.Handler {
	csp := "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: https://*.googleusercontent.com; connect-src 'self'; frame-ancestors 'none'"
	if allowEmbedding {
		csp = "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: https://*.googleusercontent.com; connect-src 'self'"
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if !allowEmbedding {
			w.Header().Set("X-Frame-Options", "DENY")
		}
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", csp)
		next.ServeHTTP(w, r)
	})
}

// logRequests logs API traffic only; static files stay quiet.
func logRequests(next http.Handler, logger *log.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			started := time.Now()
			next.ServeHTTP(w, r)
			logger.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(started).Round(time.Millisecond))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// noCache keeps the shell page fresh while allowing ETag revalidation.
func noCache(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}
