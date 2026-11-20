package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/abcdlsj/gauge/internal/engine"
	"github.com/abcdlsj/gauge/internal/providers"
	"github.com/abcdlsj/gauge/internal/store"
	"github.com/abcdlsj/gauge/internal/updater"
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
		Help: fmt.Sprintf("Minimum %d min.", floor),
	}
}

// settingSections is the single source of truth for both the settings UI and
// the POST handler's allow-list.
var settingSections = []sectionDef{
	{
		// ID matches the layout group (internal/engine/layout.go): one page per
		// subscription. The UI titles it after the plan ("Google AI Ultra").
		ID: "google", Title: "Google AI", Providers: []string{"antigravity", "google-flow", "colab"},
		Summary: "Antigravity model quota, Flow credits and Colab compute units of the Google account.",
		Fields: []fieldDef{
			{Key: "antigravity_refresh_token", Label: "Antigravity refresh token", Kind: "secret", Placeholder: "1//0g…", Help: "Read only while the IDE is closed. Leave empty to use the Gauge sign-in above.", Sensitive: true, Optional: true},
			{Key: "browser_cookies", Label: "Read cookies from Chrome", Kind: "switch", Options: []string{"chrome", "off"}, Default: "chrome", Help: "Flow and Colab reuse the Chrome sign-in. One keychain prompt."},
			{Key: "flow_cookie", Label: "Flow cookie", Kind: "secret", Multiline: true, Placeholder: "SID=…; SAPISID=…", Sensitive: true, Optional: true},
			{Key: "colab_cookie", Label: "Colab cookie", Kind: "secret", Multiline: true, Placeholder: "SID=…; HSID=…", Sensitive: true, Optional: true},
			interval("antigravity", "Antigravity", 5, 5),
			interval("google-flow", "Flow", 30, 30),
			interval("colab", "Colab", 30, 30),
		},
	},
	{
		ID: "commandcode", Title: "Command Code", Providers: []string{"commandcode"},
		Summary: "5-hour and weekly plan windows.",
		Fields: []fieldDef{
			{Key: "commandcode_api_key", Label: "API key", Kind: "secret", Placeholder: "user_…", Help: "Leave empty to use the Command Code CLI sign-in (~/.commandcode/auth.json).", Sensitive: true, Optional: true},
			interval("commandcode", "Refresh", 5, 2),
		},
	},
	{
		ID: "opendesign", Title: "Open Design", Providers: []string{"opendesign"},
		Summary: "Design Plan windows and USD balance.",
		Fields: []fieldDef{
			{Key: "opendesign_control_key", Label: "Control key", Kind: "secret", Placeholder: "ck_…", Help: "Leave empty to use the Open Design CLI sign-in (~/.amr/config.json).", Sensitive: true, Optional: true},
			interval("opendesign", "Refresh", 5, 2),
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
			{Key: "google_refresh_token", Label: "Refresh token", Kind: "secret", Placeholder: "1//0g…", Help: "Used when no GCP refresh token is set, before the ADC file.", Sensitive: true, Optional: true},
			{Key: "gcp_sa_json", Label: "Service account JSON", Kind: "secret", Multiline: true, Placeholder: `{"type":"service_account", …}`, Help: "Takes precedence over the refresh token.", Sensitive: true, Optional: true},
			interval("gcp-billing", "Billing", 360, 60),
			interval("gcp-monitoring", "Monitoring", 10, 10),
		},
	},
	{
		ID: "general", Title: "General", Providers: []string{},
		Fields: []fieldDef{
			{Key: providers.ProxySetting, Label: "Proxy", Kind: "proxy", Placeholder: "http://127.0.0.1:7890", Optional: true},
			{Key: "webhook_url", Label: "Alert webhook", Kind: "secret", Placeholder: "https://hooks.slack.com/services/…", Sensitive: true, Optional: true},
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

// instanceFieldDef resolves the settings keys that only exist for extra
// configurations ("commandcode_api_key_2", "commandcode_account_2").
func instanceFieldDef(key string) (fieldDef, bool) {
	for _, spec := range providers.InstanceSpecs() {
		for slot := 2; slot <= providers.MaxInstances(); slot++ {
			switch key {
			case providers.InstanceKey(spec.BaseKey, slot):
				return fieldDef{Key: key, Label: spec.Label, Kind: "secret", Sensitive: true, Optional: true,
					Help: "Leave empty to keep this instance unconfigured."}, true
			case providers.InstanceNameKey(spec.Provider, slot):
				return fieldDef{Key: key, Label: "Account name", Kind: "text",
					Help: "Shown in the menu bar to tell this account apart."}, true
			}
		}
	}
	return fieldDef{}, false
}

// instanceExists reports whether an extra slot has a name or a credential.
func (a *Server) instanceExists(ctx context.Context, spec providers.InstanceSpec, slot int) bool {
	for _, key := range []string{
		providers.InstanceNameKey(spec.Provider, slot),
		providers.InstanceKey(spec.BaseKey, slot),
	} {
		if v, ok, _ := a.store.Get(ctx, key); ok && v != "" {
			return true
		}
	}
	return false
}

// instanceFields appends the fields of the configured extra slots to a
// section, so the shared settings UI renders them like native fields.
func (a *Server) instanceFields(ctx context.Context, secID string) []fieldDef {
	spec, ok := providers.SpecFor(secID)
	if !ok {
		return nil
	}
	var out []fieldDef
	for slot := 2; slot <= providers.MaxInstances(); slot++ {
		if !a.instanceExists(ctx, spec, slot) {
			continue
		}
		if f, ok := instanceFieldDef(providers.InstanceNameKey(spec.Provider, slot)); ok {
			out = append(out, f)
		}
		if f, ok := instanceFieldDef(providers.InstanceKey(spec.BaseKey, slot)); ok {
			out = append(out, f)
		}
	}
	return out
}

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

// writeJSON encodes v with the standard content type.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// api bundles the dependencies shared by the HTTP handlers.
type Server struct {
	store   *store.Store
	engine  *engine.Engine
	updater *updater.Manager
	proxy   *providers.Proxy
	// client follows the proxy setting; used for avatar fetches and the proxy
	// probe.
	client *http.Client
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
			fields := append(append([]fieldDef{}, sec.Fields...), a.instanceFields(ctx, sec.ID)...)
			out := settingsSection{sectionDef: sec, Fields: make([]settingsField, 0, len(fields))}
			for _, f := range fields {
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
				f, allowed = instanceFieldDef(key)
			}
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
		if _, ok := instanceFieldDef(key); !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown setting"})
			return
		}
	}
	if err := a.store.Delete(r.Context(), key); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.afterSettingsChange(r.Context(), map[string]string{key: ""})
	writeJSON(w, http.StatusOK, map[string]any{"removed": key})
}

// handleInstance creates an extra configuration for a source. The user names
// it here; its credential is filled in on the instance page afterwards.
func (a *Server) handleInstance(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	provider := r.PathValue("provider")
	spec, ok := providers.SpecFor(provider)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "this source does not support extra instances"})
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "an account name is required"})
		return
	}
	ctx := r.Context()
	slot := 0
	for i := 2; i <= providers.MaxInstances(); i++ {
		if !a.instanceExists(ctx, spec, i) {
			slot = i
			break
		}
	}
	if slot == 0 {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "three instances is the limit"})
		return
	}
	if err := a.store.Set(ctx, providers.InstanceNameKey(provider, slot), name); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"provider": provider, "slot": slot, "name": name})
}

// handleDeleteInstance removes an extra configuration and refreshes the
// source, which drops its snapshots.
func (a *Server) handleDeleteInstance(w http.ResponseWriter, r *http.Request) {
	provider := r.PathValue("provider")
	spec, ok := providers.SpecFor(provider)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "this source does not support extra instances"})
		return
	}
	slot, err := strconv.Atoi(r.PathValue("slot"))
	if err != nil || slot < 2 || slot > providers.MaxInstances() {
		http.Error(w, "invalid slot", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	for _, key := range []string{
		providers.InstanceNameKey(spec.Provider, slot),
		providers.InstanceKey(spec.BaseKey, slot),
	} {
		if err := a.store.Delete(ctx, key); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	a.engine.ResetCooldown(provider)
	go a.engine.RunOnce(context.Background(), provider, true)
	writeJSON(w, http.StatusOK, map[string]any{"removed": slot})
}

// handleDisplay stores the order and visibility of the subscription groups
// and their cards, shared by the menu bar and the overview page.
func (a *Server) handleDisplay(w http.ResponseWriter, r *http.Request) {
	var payload engine.StoredLayout
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	clean := payload.Validate()
	raw, _ := json.Marshal(clean)
	if err := a.store.Set(r.Context(), engine.LayoutKey, string(raw)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	ov, err := a.engine.Overview(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"layout": ov.Layout})
}

// validateField rejects values the collectors could not use.
func validateField(f fieldDef, value string) error {
	switch f.Kind {
	case "proxy":
		return providers.ValidateProxy(value)
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
	// A new route should be used by the next attempt, not fifteen seconds later.
	if _, ok := changed[providers.ProxySetting]; ok && a.proxy != nil {
		a.proxy.Invalidate()
	}
	// Trigger an immediate collection for any provider whose credentials or
	// network route changed, so a user setting up a new source sees data right
	// away instead of waiting for the next polling tick.
	if targets, all := providersToRefresh(changed); all {
		a.engine.Trigger()
	} else if len(targets) > 0 {
		a.engine.Trigger(targets...)
	}
}

// providersToRefresh maps changed setting keys to the collectors that should
// re-run immediately. Non-credential preferences (icon, webhook, intervals)
// return nothing.
func providersToRefresh(changed map[string]string) (targets []string, all bool) {
	for key := range changed {
		switch {
		case key == providers.ProxySetting:
			return nil, true
		case key == "antigravity_refresh_token" || strings.HasPrefix(key, "antigravity_refresh_token_"):
			targets = append(targets, "antigravity")
		case key == "flow_cookie":
			targets = append(targets, "google-flow")
		case key == "colab_cookie":
			targets = append(targets, "colab")
		case key == "browser_cookies":
			targets = append(targets, "google-flow", "colab")
		case key == "commandcode_api_key" || strings.HasPrefix(key, "commandcode_api_key_"):
			targets = append(targets, "commandcode")
		case key == "opendesign_control_key" || strings.HasPrefix(key, "opendesign_control_key_"):
			targets = append(targets, "opendesign")
		case key == "gcp_project_id" || key == "gcp_billing_table" || key == "gcp_initial_credits" ||
			key == "gcp_currency" || key == "google_refresh_token" || key == "gcp_sa_json":
			targets = append(targets, "gcp-billing", "gcp-monitoring")
		}
	}
	return targets, false
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
	_ = a.store.Delete(ctx, fmt.Sprintf("antigravity_account_%d", slot))
	a.engine.ResetCooldown()
	// Re-collect now: until it runs, the removed account keeps its group and
	// its "@N" readings.
	go a.engine.RunOnce(context.Background(), "antigravity", true)
	writeJSON(w, http.StatusOK, map[string]any{"removed": slot})
}

// handleAvatar serves the cached profile image. Browsers cache it, so the
// window paints instantly on the next launch; the pages add the account's
// email as a query so a slot that changes account does not keep the old face.
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
		// Fetch Google's copy here instead of redirecting: the webview's
		// content policy and App Transport Security both dislike a cross-origin
		// image hop, so the avatar silently failed and fell back to initials.
		if u, err := url.Parse(account.Picture); err == nil && u.Scheme == "https" && strings.HasSuffix(u.Hostname(), ".googleusercontent.com") {
			client := a.client
			if client == nil {
				client = &http.Client{Timeout: 5 * time.Second}
			}
			req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, u.String(), nil)
			if err != nil {
				break
			}
			req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) Gauge")
			resp, err := client.Do(req)
			if err != nil || resp.StatusCode != http.StatusOK {
				if resp != nil {
					resp.Body.Close()
				}
				break
			}
			w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
			w.Header().Set("Cache-Control", "public, max-age=3600")
			_, _ = io.Copy(w, io.LimitReader(resp.Body, 512<<10))
			resp.Body.Close()
			return
		}
		http.Redirect(w, r, account.Picture, http.StatusFound)
		return
	}
	http.NotFound(w, r)
}

// handleProxy reports the route upstream requests take.
func (a *Server) handleProxy(w http.ResponseWriter, r *http.Request) {
	cfg := resolveConfig(a.proxy, r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"config": cfg, "description": cfg.Describe()})
}

// proxyReport is the answer of POST /api/proxy/test: the route that is in
// effect, whether it reaches Google, and any local proxies that do.
type proxyReport struct {
	Config     providers.ProxyConfig      `json:"config"`
	OK         bool                       `json:"ok"`
	LatencyMS  int64                      `json:"latency_ms"`
	Error      string                     `json:"error,omitempty"`
	Candidates []providers.ProxyCandidate `json:"candidates,omitempty"`
}

// handleProxyTest probes a proxy (body {"proxy": "..."}) or the effective route
// and, in the same round trip, scans the usual local proxy ports. Scanning is
// why the settings page fills the field with one click on a new machine.
func (a *Server) handleProxyTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Proxy *string `json:"proxy"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body)
	}

	cfg := resolveConfig(a.proxy, r.Context())
	if body.Proxy != nil {
		raw := strings.TrimSpace(*body.Proxy)
		if err := providers.ValidateProxy(raw); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		cfg = providers.ResolveProxy(raw)
	}

	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()

	report := proxyReport{Config: cfg, Candidates: providers.DetectProxies(ctx)}
	if latency, err := providers.Probe(ctx, cfg, 5*time.Second); err != nil {
		report.Error = err.Error()
	} else {
		report.OK = true
		report.LatencyMS = latency.Milliseconds()
	}
	writeJSON(w, http.StatusOK, report)
}

// resolveConfig falls back to a fresh resolver when no store-backed one exists.
func resolveConfig(p *providers.Proxy, ctx context.Context) providers.ProxyConfig {
	if p == nil {
		return providers.ResolveProxy("")
	}
	return p.Config(ctx)
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

// handleUpdate returns the cached auto-update state without hitting GitHub.
func (a *Server) handleUpdate(w http.ResponseWriter, r *http.Request) {
	if a.updater == nil {
		writeJSON(w, http.StatusOK, updater.Status{State: updater.StateIdle})
		return
	}
	writeJSON(w, http.StatusOK, a.updater.Status())
}

// handleUpdateCheck triggers an immediate GitHub release check (subject to cooldown).
func (a *Server) handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	if a.updater == nil {
		writeJSON(w, http.StatusOK, updater.Status{State: updater.StateIdle})
		return
	}
	writeJSON(w, http.StatusOK, a.updater.CheckManual(r.Context()))
}

// handleUpdateApply starts the background download, verification, and silent restart.
func (a *Server) handleUpdateApply(w http.ResponseWriter, r *http.Request) {
	if a.updater == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "updater unavailable"})
		return
	}
	if err := a.updater.Apply(); err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusAccepted, a.updater.Status())
}
