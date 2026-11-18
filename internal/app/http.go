package app

import (
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/abcdlsj/gauge/internal/engine"
	"github.com/abcdlsj/gauge/internal/store"
)

// fieldDef describes one credential/setting input rendered by the panel.
type fieldDef struct {
	Key         string   `json:"key"`
	Label       string   `json:"label"`
	Group       string   `json:"group"`
	Kind        string   `json:"kind"` // text, password, number, textarea, url
	Placeholder string   `json:"placeholder"`
	Help        string   `json:"help"`
	Sensitive   bool     `json:"sensitive"`
	Optional    bool     `json:"optional,omitempty"`
	Default     string   `json:"default,omitempty"`
	Options     []string `json:"options,omitempty"`
}

// defaultColabEndpointHint keeps the placeholder short; the collector falls back
// to the same URL when the field is empty.
const defaultColabEndpointHint = "https://colab.clients6.google.com/$rpc/.../GetUserInfo"

// settingFields is the single source of truth for both the settings form and
// the POST handler's allow-list.
var settingFields = []fieldDef{
	{Key: "google_refresh_token", Label: "Shared refresh token", Group: "Google account", Kind: "password", Placeholder: "1//0g...", Help: "Fallback for GCP billing and Monitoring. ADC is discovered automatically.", Sensitive: true, Optional: true},
	{Key: "gcp_sa_json", Label: "Service account JSON", Group: "Google account", Kind: "textarea", Placeholder: `{"type":"service_account", ...}`, Help: "Takes precedence over the refresh token for BigQuery and Monitoring.", Sensitive: true, Optional: true},

	{Key: "antigravity_refresh_token", Label: "Antigravity refresh token", Group: "Antigravity", Kind: "password", Placeholder: "or use Sign in with Google above", Help: "Minted by Sign in with Google. Only needed when the IDE is closed.", Sensitive: true, Optional: true},

	{Key: "gcp_project_id", Label: "GCP project ID", Group: "GCP billing", Kind: "text", Placeholder: "my-project", Help: "Billing export and Monitoring queries run against this project."},
	{Key: "gcp_billing_table", Label: "Billing export table", Group: "GCP billing", Kind: "text", Placeholder: "project.dataset.gcp_billing_export_v1_XXXXXX", Help: "Standard usage cost export in BigQuery."},
	{Key: "gcp_initial_credits", Label: "Initial credit amount", Group: "GCP billing", Kind: "number", Placeholder: "447.27", Help: "Starting balance used to derive the remaining amount."},
	{Key: "gcp_currency", Label: "Currency", Group: "GCP billing", Kind: "text", Placeholder: "HKD", Help: "Display currency for the credit card.", Default: "HKD"},

	{Key: "browser_cookies", Label: "Cookie source", Group: "Colab", Kind: "select", Options: []string{"chrome", "off"}, Help: "chrome reads the local Chrome profile (one keychain prompt); off uses the pasted cookie.", Default: "chrome"},
	{Key: "colab_cookie", Label: "Pasted cookie", Group: "Colab", Kind: "textarea", Placeholder: "SID=...; HSID=...", Help: "Only needed when the browser cookie source is off.", Sensitive: true, Optional: true},

	{Key: "flow_cookie", Label: "Pasted cookie", Group: "Google Flow", Kind: "textarea", Placeholder: "SID=...; SAPISID=...", Help: "Only needed when the browser cookie source is off.", Sensitive: true, Optional: true},

	{Key: "refresh_antigravity_minutes", Label: "Antigravity interval", Group: "Refresh", Kind: "number", Placeholder: "5", Help: "Minutes between quota polls (minimum 5).", Default: "5"},
	{Key: "refresh_colab_minutes", Label: "Colab interval", Group: "Refresh", Kind: "number", Placeholder: "30", Help: "Minutes between Colab polls (minimum 30).", Default: "30"},
	{Key: "refresh_google-flow_minutes", Label: "Google Flow interval", Group: "Refresh", Kind: "number", Placeholder: "30", Help: "Minutes between Flow polls (minimum 30).", Default: "30"},
	{Key: "refresh_gcp-monitoring_minutes", Label: "Monitoring interval", Group: "Refresh", Kind: "number", Placeholder: "10", Help: "Minutes between Monitoring polls (minimum 10).", Default: "10"},
	{Key: "refresh_gcp-billing_minutes", Label: "Billing interval", Group: "Refresh", Kind: "number", Placeholder: "360", Help: "Minutes between BigQuery polls (minimum 60).", Default: "360"},

	{Key: "webhook_url", Label: "Alert webhook", Group: "Alerts", Kind: "password", Placeholder: "https://hooks.slack.com/services/...", Help: "Receives a JSON payload when a provider changes state.", Sensitive: true, Optional: true},
}

var fieldByKey = func() map[string]fieldDef {
	m := make(map[string]fieldDef, len(settingFields))
	for _, f := range settingFields {
		m[f.Key] = f
	}
	return m
}()

// settingsField is one field plus its stored state.
type settingsField struct {
	fieldDef
	Value      string `json:"value"`
	Configured bool   `json:"configured"`
}

// mask keeps a short, non-reversible hint for secrets.
func mask(v string) string {
	if v == "" {
		return ""
	}
	if len(v) <= 4 {
		return "••••"
	}
	return "••••" + v[len(v)-4:]
}

// requireBasicAuth wraps a handler with HTTP Basic authentication. Empty
// credentials turn the middleware into a pass-through.
func requireBasicAuth(next http.Handler, user, pass string) http.Handler {
	if user == "" && pass == "" {
		return next
	}
	expected := user + "\x00" + pass
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Google's redirect cannot carry Basic credentials; the OAuth state
		// parameter protects the callback instead.
		if r.URL.Path == "/oauth/callback" {
			next.ServeHTTP(w, r)
			return
		}
		u, p, ok := r.BasicAuth()
		got := u + "\x00" + p
		if !ok || subtle.ConstantTimeCompare([]byte(got), []byte(expected)) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="GPanel", charset="UTF-8"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
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

// writeJSON encodes v with the standard content type.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// api bundles the dependencies shared by the HTTP handlers.
type Server struct {
	store  *store.Store
	engine *engine.Engine
	log    *log.Logger
	start  time.Time
}

// handleOverview returns the full dashboard payload.
func (a *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	ov, err := a.engine.Overview(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, ov)
}

// handleSettings reads or writes the credential table.
func (a *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	switch r.Method {
	case http.MethodGet:
		fields := make([]settingsField, 0, len(settingFields))
		for _, f := range settingFields {
			raw, ok, err := a.store.Get(ctx, f.Key)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			configured := ok && raw != ""
			sf := settingsField{fieldDef: f, Configured: configured}
			switch {
			case !configured && f.Default != "":
				sf.Value = f.Default
			case f.Sensitive:
				sf.Value = mask(raw)
			default:
				sf.Value = raw
			}
			fields = append(fields, sf)
		}
		writeJSON(w, http.StatusOK, map[string]any{"fields": fields})
	case http.MethodPost:
		var payload map[string]string
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10)).Decode(&payload); err != nil {
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}
		saved := 0
		for key, value := range payload {
			if _, allowed := fieldByKey[key]; !allowed {
				continue
			}
			if strings.TrimSpace(value) == "" {
				continue
			}
			if err := a.store.Set(ctx, key, strings.TrimSpace(value)); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			saved++
		}
		// New credentials should be usable right away, not after the cooldown.
		a.engine.ResetCooldown()
		// A fresh cookie resumes a paused Colab collector immediately.
		if _, ok := payload["colab_cookie"]; ok {
			_ = a.store.Delete(ctx, "colab_auth_state")
			_ = a.store.Delete(ctx, "colab_cookie_fp")
		}
		writeJSON(w, http.StatusOK, map[string]any{"saved": saved})
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleRefresh collects a single provider on demand.
func (a *Server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("provider")
	run, err := a.engine.RunOnce(r.Context(), name, true)
	if err == engine.ErrCooldown {
		w.Header().Set("Retry-After", "60")
		writeJSON(w, http.StatusTooManyRequests, map[string]any{
			"error":       "cooldown",
			"retry_after": 60,
			"provider":    name,
		})
		return
	}
	if errors.Is(err, engine.ErrUnknownProvider) {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "unknown provider"})
		return
	}
	writeJSON(w, http.StatusOK, run)
}

// handleRefreshAll collects every provider, honouring per-provider cooldowns.
func (a *Server) handleRefreshAll(w http.ResponseWriter, r *http.Request) {
	runs := a.engine.RefreshAll(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
}

// handleDeleteAccount drops an OAuth-connected account.
func (a *Server) handleDeleteAccount(w http.ResponseWriter, r *http.Request) {
	slot, err := strconv.Atoi(r.PathValue("slot"))
	if err != nil || slot < 1 || slot > 3 {
		http.Error(w, "invalid slot", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	key := "antigravity_refresh_token"
	if slot > 1 {
		key = fmt.Sprintf("antigravity_refresh_token_%d", slot)
	}
	if err := a.store.Delete(ctx, key); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if slot > 1 {
		_ = a.store.Delete(ctx, fmt.Sprintf("antigravity_account_%d", slot))
	}
	a.engine.ResetCooldown()
	writeJSON(w, http.StatusOK, map[string]any{"removed": slot})
}

// handleAvatar serves the cached profile image. Browsers cache it, so the
// window paints instantly on the next launch.
func (a *Server) handleAvatar(w http.ResponseWriter, r *http.Request) {
	slot, err := strconv.Atoi(r.PathValue("slot"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	for _, account := range a.engine.Accounts() {
		if account.Slot != slot || account.Picture == "" {
			continue
		}
		if rest, ok := strings.CutPrefix(account.Picture, "data:"); ok {
			if media, data, found := strings.Cut(rest, ";base64,"); found {
				raw, err := base64.StdEncoding.DecodeString(data)
				if err != nil {
					break
				}
				w.Header().Set("Content-Type", media)
				w.Header().Set("Cache-Control", "public, max-age=86400")
				_, _ = w.Write(raw)
				return
			}
		}
		http.Redirect(w, r, account.Picture, http.StatusFound)
		return
	}
	http.NotFound(w, r)
}

// handleHealth is a cheap liveness probe that touches no upstream API.
func (a *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ov, err := a.engine.Overview(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"status": "degraded", "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":     "ok",
		"uptime_sec": int(time.Since(a.start).Seconds()),
		"quotas":     len(ov.Quotas),
		"runs":       ov.Runs,
	})
}
