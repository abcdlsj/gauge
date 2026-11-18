package app

import (
	"context"
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
	Kind        string   `json:"kind"` // text, number, secret, switch, select
	Placeholder string   `json:"placeholder,omitempty"`
	Help        string   `json:"help,omitempty"`
	Sensitive   bool     `json:"sensitive"`
	Multiline   bool     `json:"multiline,omitempty"`
	Optional    bool     `json:"optional,omitempty"`
	Default     string   `json:"default,omitempty"`
	Options     []string `json:"options,omitempty"`
	Unit        string   `json:"unit,omitempty"` // suffix shown after numbers
	Min         int      `json:"min,omitempty"`
}

// sectionDef groups the fields of one source. Providers ties the section to
// the collectors it configures, so the UI can show their state and cards.
type sectionDef struct {
	ID        string     `json:"id"`
	Title     string     `json:"title"`
	Summary   string     `json:"summary,omitempty"`
	Providers []string   `json:"providers"`
	Fields    []fieldDef `json:"-"`
}

func interval(provider, label string, def, floor int) fieldDef {
	return fieldDef{
		Key: "refresh_" + provider + "_minutes", Label: label, Kind: "number", Unit: "min",
		Default: strconv.Itoa(def), Min: floor,
		Help: fmt.Sprintf("Minimum %d minutes.", floor),
	}
}

// settingSections is the single source of truth for both the settings UI and
// the POST handler's allow-list.
var settingSections = []sectionDef{
	{
		ID: "antigravity", Title: "Antigravity", Providers: []string{"antigravity"},
		Summary: "Gemini and Claude model quota. Read from the running IDE first; the token is only needed when it is closed.",
		Fields: []fieldDef{
			{Key: "antigravity_refresh_token", Label: "Refresh token", Kind: "secret", Placeholder: "1//0g…", Help: "Minted by Sign in with Google.", Sensitive: true, Optional: true},
			interval("antigravity", "Refresh every", 5, 5),
		},
	},
	{
		ID: "commandcode", Title: "Command Code", Providers: []string{"commandcode"},
		Summary: "5-hour and weekly plan windows.",
		Fields: []fieldDef{
			{Key: "commandcode_api_key", Label: "API key", Kind: "secret", Placeholder: "user_…", Help: "Leave empty to use the Command Code CLI sign-in (~/.commandcode/auth.json).", Sensitive: true, Optional: true},
			interval("commandcode", "Refresh every", 5, 2),
		},
	},
	{
		ID: "opendesign", Title: "Open Design", Providers: []string{"opendesign"},
		Summary: "Design Plan windows and USD balance.",
		Fields: []fieldDef{
			{Key: "opendesign_control_key", Label: "Control key", Kind: "secret", Placeholder: "ck_…", Help: "Leave empty to use the Open Design CLI sign-in (~/.amr/config.json).", Sensitive: true, Optional: true},
			interval("opendesign", "Refresh every", 5, 2),
		},
	},
	{
		ID: "colab", Title: "Colab", Providers: []string{"colab"},
		Summary: "Compute-unit balance.",
		Fields: []fieldDef{
			{Key: "colab_cookie", Label: "Pasted cookie", Kind: "secret", Multiline: true, Placeholder: "SID=…; HSID=…", Help: "Only used when Chrome cookies are off.", Sensitive: true, Optional: true},
			interval("colab", "Refresh every", 30, 30),
		},
	},
	{
		ID: "google-flow", Title: "Google Flow", Providers: []string{"google-flow"},
		Summary: "Video credit balance.",
		Fields: []fieldDef{
			{Key: "flow_cookie", Label: "Pasted cookie", Kind: "secret", Multiline: true, Placeholder: "SID=…; SAPISID=…", Help: "Only used when Chrome cookies are off.", Sensitive: true, Optional: true},
			interval("google-flow", "Refresh every", 30, 30),
		},
	},
	{
		ID: "gcp", Title: "Google Cloud", Providers: []string{"gcp-billing", "gcp-monitoring"},
		Summary: "Billing-export credits and Gemini API call counts.",
		Fields: []fieldDef{
			{Key: "gcp_project_id", Label: "Project ID", Kind: "text", Placeholder: "my-project"},
			{Key: "gcp_billing_table", Label: "Billing export table", Kind: "text", Placeholder: "project.dataset.gcp_billing_export_v1_…", Help: "Standard usage cost export in BigQuery."},
			{Key: "gcp_initial_credits", Label: "Initial credits", Kind: "number", Placeholder: "447.27", Help: "Starting balance used to derive the remaining amount."},
			{Key: "gcp_currency", Label: "Currency", Kind: "text", Placeholder: "HKD", Default: "HKD"},
			{Key: "google_refresh_token", Label: "Refresh token", Kind: "secret", Placeholder: "1//0g…", Help: "Fallback when no ADC is found.", Sensitive: true, Optional: true},
			{Key: "gcp_sa_json", Label: "Service account JSON", Kind: "secret", Multiline: true, Placeholder: `{"type":"service_account", …}`, Help: "Takes precedence over the refresh token.", Sensitive: true, Optional: true},
			interval("gcp-billing", "Billing refresh", 360, 60),
			interval("gcp-monitoring", "Monitoring refresh", 10, 10),
		},
	},
	{
		ID: "general", Title: "General", Providers: []string{},
		Fields: []fieldDef{
			{Key: "browser_cookies", Label: "Read Google cookies from Chrome", Kind: "switch", Options: []string{"chrome", "off"}, Default: "chrome", Help: "Used by Colab and Flow. One keychain prompt."},
			{Key: "webhook_url", Label: "Alert webhook", Kind: "secret", Placeholder: "https://hooks.slack.com/services/…", Help: "Receives JSON when a source changes state.", Sensitive: true, Optional: true},
			// The settings UI draws this one as a picker in the Appearance group.
			{Key: IconSetting, Label: "Icon", Kind: "select", Options: []string{IconGauge, IconClassic}, Default: IconGauge, Help: "Menu bar and window mark."},
		},
	},
}

var fieldByKey = func() map[string]fieldDef {
	m := map[string]fieldDef{}
	for _, sec := range settingSections {
		for _, f := range sec.Fields {
			m[f.Key] = f
		}
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
	// onChange is Config.OnSettingsChange.
	onChange func(changed map[string]string)
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

// settingsSection is one section plus the stored state of its fields.
type settingsSection struct {
	sectionDef
	Fields []settingsField `json:"fields"`
}

// handleSettings reads every section, or writes a batch of values.
func (a *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	switch r.Method {
	case http.MethodGet:
		sections := make([]settingsSection, 0, len(settingSections))
		for _, sec := range settingSections {
			out := settingsSection{sectionDef: sec, Fields: make([]settingsField, 0, len(sec.Fields))}
			for _, f := range sec.Fields {
				raw, ok, err := a.store.Get(ctx, f.Key)
				if err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
				configured := ok && raw != ""
				sf := settingsField{fieldDef: f, Configured: configured}
				switch {
				case !configured:
					sf.Value = f.Default
				case f.Sensitive:
					sf.Value = mask(raw)
				default:
					sf.Value = raw
				}
				out.Fields = append(out.Fields, sf)
			}
			sections = append(sections, out)
		}
		writeJSON(w, http.StatusOK, map[string]any{"sections": sections})
	case http.MethodPost:
		var payload map[string]string
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10)).Decode(&payload); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
			return
		}
		clean := make(map[string]string, len(payload))
		for key, value := range payload {
			f, allowed := fieldByKey[key]
			if !allowed {
				continue
			}
			value = strings.TrimSpace(value)
			if value == "" {
				continue
			}
			if err := validateField(f, value); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error(), "key": key})
				return
			}
			clean[key] = value
		}
		for key, value := range clean {
			if err := a.store.Set(ctx, key, value); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
		a.afterSettingsChange(ctx, clean)
		writeJSON(w, http.StatusOK, map[string]any{"saved": len(clean)})
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleDeleteSetting clears one value, so the field falls back to its
// default (or, for a credential, to automatic discovery).
func (a *Server) handleDeleteSetting(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if _, allowed := fieldByKey[key]; !allowed {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown setting"})
		return
	}
	if err := a.store.Delete(r.Context(), key); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.afterSettingsChange(r.Context(), map[string]string{key: ""})
	writeJSON(w, http.StatusOK, map[string]any{"removed": key})
}

// validateField rejects values the collectors could not use.
func validateField(f fieldDef, value string) error {
	switch f.Kind {
	case "number":
		n, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return fmt.Errorf("%s must be a number", f.Label)
		}
		if f.Min > 0 && n < float64(f.Min) {
			return fmt.Errorf("%s must be at least %d %s", f.Label, f.Min, f.Unit)
		}
	case "switch", "select":
		for _, o := range f.Options {
			if o == value {
				return nil
			}
		}
		return fmt.Errorf("%s must be one of %s", f.Label, strings.Join(f.Options, ", "))
	}
	return nil
}

// afterSettingsChange makes new values usable right away.
func (a *Server) afterSettingsChange(ctx context.Context, changed map[string]string) {
	if a.onChange != nil {
		a.onChange(changed)
	}
	// New credentials should be usable right away, not after the cooldown.
	a.engine.ResetCooldown()
	// A fresh cookie resumes a paused collector immediately.
	if _, ok := changed["colab_cookie"]; ok {
		_ = a.store.Delete(ctx, "colab_auth_state")
		_ = a.store.Delete(ctx, "colab_cookie_fp")
	}
	if _, ok := changed["flow_cookie"]; ok {
		_ = a.store.Delete(ctx, "flow_auth_state")
		_ = a.store.Delete(ctx, "flow_cookie_fp")
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
