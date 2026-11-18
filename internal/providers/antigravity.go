package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/abcdlsj/gauge/internal/model"
)

// Antigravity collects per-model quota from the Cloud Code private API, the same
// endpoint the Antigravity IDE's language server calls.
//
// Findings that shape this file (verified against AntigravityqQuotaWatcher,
// Antigravity-Manager and antigravity-panel):
//   - the quota endpoint only accepts tokens minted for the Antigravity OAuth
//     client; tokens from other Google clients are rejected with PERMISSION_DENIED
//   - the API lives on three hosts, daily first, then the sandbox, then prod
//   - loadCodeAssist with metadata.ideType=ANTIGRAVITY reveals the subscription
//     tier and the cloudaicompanion project
//   - retrieveUserQuotaSummary exposes the grouped 5h / weekly quota pools
type Antigravity struct{}

// Antigravity's own OAuth client. Installed-app client secrets are not secret
// by Google's own definition; the same pair is published by the OSS projects
// above so the panel can authenticate the account owner directly.
const (
	antigravityClientID     = "1071006060591-tmhssin2h21lcre235vtolojh4g403ep.apps.googleusercontent.com"
	antigravityClientSecret = "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"
)

// AntigravityScopes is the scope set the IDE itself requests.
var AntigravityScopes = []string{
	"https://www.googleapis.com/auth/cloud-platform",
	"https://www.googleapis.com/auth/userinfo.email",
	"https://www.googleapis.com/auth/userinfo.profile",
	"https://www.googleapis.com/auth/cclog",
	"https://www.googleapis.com/auth/experimentsandconfigs",
}

// AntigravityClientID exposes the OAuth client used for the sign-in flow.
func AntigravityClientID() string { return antigravityClientID }

// AntigravityClientSecret exposes the matching client secret.
func AntigravityClientSecret() string { return antigravityClientSecret }

// cloudCodeHosts is tried in order; the daily host is what the IDE uses.
var cloudCodeHosts = []string{
	"https://daily-cloudcode-pa.googleapis.com",
	"https://daily-cloudcode-pa.sandbox.googleapis.com",
	"https://cloudcode-pa.googleapis.com",
}

func (Antigravity) Name() string            { return "antigravity" }
func (Antigravity) Title() string           { return "Antigravity" }
func (Antigravity) Interval() time.Duration { return 5 * time.Minute }

type antigravityModel struct {
	DisplayName string `json:"displayName"`
	QuotaInfo   *struct {
		RemainingFraction *float64 `json:"remainingFraction"`
		ResetTime         string   `json:"resetTime"`
	} `json:"quotaInfo"`
}

type antigravityTier struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type loadCodeAssistResponse struct {
	Project      string            `json:"cloudaicompanionProject"`
	CurrentTier  *antigravityTier  `json:"currentTier"`
	PaidTier     *antigravityTier  `json:"paidTier"`
	AllowedTiers []antigravityTier `json:"allowedTiers"`
}

// Collect prefers the running IDE: its language server reports credits and the
// plan without any OAuth round trip. The Cloud Code API is layered on top for
// per-model quota, and is allowed to fail on its own.
func (Antigravity) Collect(ctx context.Context, d *Deps) ([]model.Snapshot, error) {
	local, labels, credits, localPools, primary := collectLocal(ctx, d)
	models, remotePools, info, err := collectRemote(ctx, d, labels, SourceAntigravity)
	if err != nil {
		d.Log.Printf("antigravity: remote quota unavailable: %v", err)
		if len(local) > 0 || len(credits) > 0 || len(localPools) > 0 {
			// The IDE still knows both pool windows even when the API cannot be
			// reached, so keep the family cards instead of dropping to credits.
			if len(localPools) > 0 {
				local = append(local, mergeFamilies(nil, localPools, credits)...)
			} else if card, ok := otherCard(credits, nil); ok {
				local = append(local, card)
			}
			d.SetAccounts(ctx, collectAccounts(ctx, d, primary))
			return local, nil
		}
		return nil, err
	}
	if primary.Email == "" {
		// The IDE is closed; the API still tells us the plan.
		primary.Tier = firstNonEmpty(tierLabel(tierName(info)), primary.Tier)
		primary.TierID = tierName(info)
	}
	// The IDE knows both windows; the REST API only reports the third-party
	// group, so its pools are a fallback for when the IDE is closed.
	pools := localPools
	if len(pools) == 0 {
		pools = remotePools
	}
	result := append(local, mergeFamilies(models, pools, credits)...)

	// Extra accounts come from the OAuth flow; they only have the remote API.
	extra, extraAccounts := collectExtraAccounts(ctx, d)
	result = append(result, extra...)

	d.SetAccounts(ctx, append(collectAccounts(ctx, d, primary), extraAccounts...))
	return result, nil
}

// maxAccounts bounds how many identities the panel will poll.
const maxAccounts = 3

// collectExtraAccounts fetches quota for accounts signed in through OAuth.
func collectExtraAccounts(ctx context.Context, d *Deps) ([]model.Snapshot, []model.Account) {
	var (
		snaps    []model.Snapshot
		accounts []model.Account
	)
	for slot := 2; slot <= maxAccounts; slot++ {
		src := AuthSource(fmt.Sprintf("antigravity-%d", slot))
		token, ok, _ := d.Store.Get(ctx, fmt.Sprintf("antigravity_refresh_token_%d", slot))
		if !ok || token == "" {
			continue
		}

		models, pools, info, err := collectRemote(ctx, d, nil, src)
		if err != nil {
			d.Log.Printf("antigravity: account %d: %v", slot, err)
			continue
		}

		label := d.accountLabel(ctx, slot)
		for _, sn := range mergeFamilies(models, pools, nil) {
			sn.Metric = fmt.Sprintf("%s@%d", sn.Metric, slot)
			sn.Account = label.Email
			snaps = append(snaps, sn)
		}
		label.Slot = slot
		if label.Tier == "" {
			label.Tier = tierLabel(tierName(info))
			label.TierID = tierName(info)
		}
		accounts = append(accounts, label)
	}
	return snaps, accounts
}

// collectAccounts assembles the identity list shown in the header.
func collectAccounts(ctx context.Context, d *Deps, primary model.Account) []model.Account {
	var out []model.Account
	if primary.Email != "" {
		out = append(out, primary)
	}
	for slot := 2; slot <= maxAccounts; slot++ {
		token, ok, _ := d.Store.Get(ctx, fmt.Sprintf("antigravity_refresh_token_%d", slot))
		if !ok || token == "" {
			continue
		}
		label := d.accountLabel(ctx, slot)
		label.Slot = slot
		out = append(out, label)
	}
	return out
}

// collectRemote reads the Cloud Code quota API. labels maps the IDE's model ids
// to display names; hasTier suppresses a duplicate subscription card.
func collectRemote(ctx context.Context, d *Deps, labels map[string]string, src AuthSource) ([]model.Snapshot, []poolSeries, loadCodeAssistResponse, error) {
	var info loadCodeAssistResponse
	auth, err := d.GoogleAuth(ctx, AntigravityScopes, src)
	if err != nil {
		return nil, nil, info, err
	}

	info, err = loadCodeAssist(ctx, d, auth)
	if err != nil {
		return nil, nil, info, err
	}

	payload := map[string]any{}
	if info.Project != "" {
		payload["project"] = info.Project
	}
	var modelsResp struct {
		Models map[string]antigravityModel `json:"models"`
	}
	body, err := postCloudCode(ctx, d, auth, "/v1internal:fetchAvailableModels", payload)
	if err != nil {
		return nil, nil, info, err
	}
	if err := json.Unmarshal(body, &modelsResp); err != nil {
		return nil, nil, info, fmt.Errorf("antigravity: decode models: %w", err)
	}

	now := time.Now().UTC()
	out := make([]model.Snapshot, 0, len(modelsResp.Models)+3)

	tier := tierName(info)
	if tier != "" {
		_ = d.Store.Set(ctx, "antigravity_tier", tier)
	}

	names := make([]string, 0, len(modelsResp.Models))
	for name := range modelsResp.Models {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		m := modelsResp.Models[name]
		if m.QuotaInfo == nil || m.QuotaInfo.RemainingFraction == nil {
			// Auto-routing aliases carry no quota bucket of their own.
			continue
		}
		metric := strings.TrimPrefix(name, "models/")
		if !userFacingModel(metric) {
			// chat_*, tab_* and other internal catalogue entries.
			continue
		}
		label := labels[metric]
		if label == "" {
			label = displayName(name, m.DisplayName)
		}
		sn := model.Snapshot{
			Provider:  "antigravity",
			Metric:    metric,
			Label:     label,
			Remaining: clamp01(*m.QuotaInfo.RemainingFraction) * 100,
			Total:     100,
			Unit:      "%",
			Status:    model.StatusOK,
			UpdatedAt: now,
		}
		if t, err := parseTime(m.QuotaInfo.ResetTime); err == nil {
			sn.ResetAt = &t
		}
		if sn.Remaining <= 0 {
			sn.Status = model.StatusWarning
		}
		out = append(out, sn)
	}

	pools := quotaSummary(ctx, d, auth)
	if len(out) == 0 && len(pools) == 0 {
		return nil, nil, info, fmt.Errorf("antigravity: no model quota returned for this account")
	}
	return out, pools, info, nil
}

// loadCodeAssist resolves the project and the paid/current tier.
func loadCodeAssist(ctx context.Context, d *Deps, auth *Auth) (loadCodeAssistResponse, error) {
	var info loadCodeAssistResponse
	body, err := postCloudCode(ctx, d, auth, "/v1internal:loadCodeAssist", map[string]any{
		"metadata": map[string]string{"ideType": "ANTIGRAVITY"},
	})
	if err != nil {
		return info, err
	}
	if err := json.Unmarshal(body, &info); err != nil {
		return info, fmt.Errorf("antigravity: decode loadCodeAssist: %w", err)
	}
	return info, nil
}

// tierName prefers the paid tier, which is the authoritative plan.
func tierName(info loadCodeAssistResponse) string {
	if info.PaidTier != nil && info.PaidTier.ID != "" {
		return info.PaidTier.ID
	}
	if info.CurrentTier != nil && info.CurrentTier.ID != "" {
		return info.CurrentTier.ID
	}
	for _, t := range info.AllowedTiers {
		if t.ID != "" {
			return t.ID
		}
	}
	return ""
}

// poolSeries is one window of a grouped quota pool, e.g. Claude & GPT weekly.
type poolSeries struct {
	family    string
	label     string
	remaining float64
	reset     *time.Time
}

// quotaSummary returns the grouped 5-hour / weekly pools. Best effort: older
// accounts get 404 here and that is fine.
func quotaSummary(ctx context.Context, d *Deps, auth *Auth) []poolSeries {
	body, err := postCloudCode(ctx, d, auth, "/v1internal:retrieveUserQuotaSummary", map[string]any{})
	if err != nil {
		if !errors.Is(err, ErrExpiredAuth) {
			d.Log.Printf("antigravity: quota summary unavailable: %v", err)
		}
		return nil
	}
	var resp struct {
		Groups []struct {
			DisplayName string `json:"displayName"`
			Buckets     []struct {
				BucketID          string   `json:"bucketId"`
				Window            string   `json:"window"`
				RemainingFraction *float64 `json:"remainingFraction"`
				ResetTime         string   `json:"resetTime"`
				DisplayName       string   `json:"displayName"`
			} `json:"buckets"`
		} `json:"groups"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		d.Log.Printf("antigravity: decode quota summary: %v", err)
		return nil
	}

	var out []poolSeries
	for _, g := range resp.Groups {
		family := groupFamily(g.DisplayName)
		for _, b := range g.Buckets {
			if b.RemainingFraction == nil {
				continue
			}
			entry := poolSeries{
				family:    family,
				label:     windowLabel(b.Window),
				remaining: clamp01(*b.RemainingFraction) * 100,
			}
			if t, err := parseTime(b.ResetTime); err == nil {
				entry.reset = &t
			}
			out = append(out, entry)
		}
	}
	return out
}

// windowLabel normalises a bucket window into a short, readable name.
func windowLabel(window string) string {
	w := strings.ToLower(strings.TrimSpace(window))
	switch {
	case w == "":
		return "5-hour"
	case strings.HasPrefix(w, "5") || strings.Contains(w, "five hour"):
		return "5-hour"
	case strings.Contains(w, "week") || strings.Contains(w, "7d") || strings.Contains(w, "168"):
		return "weekly"
	}
	return window
}

// groupFamily maps a quota-summary group name onto a model family.
func groupFamily(name string) string {
	l := strings.ToLower(name)
	switch {
	case strings.Contains(l, "claude") || strings.Contains(l, "gpt"):
		return "claude"
	case strings.Contains(l, "gemini"):
		return "gemini"
	}
	return "other"
}

// poolLabel renders a grouped quota bucket the way people describe it:
// "Claude + GPT · 5-hour".
func poolLabel(group, window string) string {
	name := strings.TrimSpace(group)
	if strings.Contains(strings.ToLower(name), "claude") {
		name = "Claude + GPT"
	}
	if name == "" {
		name = "Shared pool"
	}
	w := strings.ToLower(strings.TrimSpace(window))
	switch {
	case strings.HasPrefix(w, "5") || strings.Contains(w, "5h") || strings.Contains(w, "five hour"):
		return name + " · 5-hour"
	case strings.Contains(w, "week") || strings.Contains(w, "7d") || strings.Contains(w, "168"):
		return name + " · weekly"
	case w == "":
		return name
	}
	return name + " · " + window
}

// postCloudCode walks the host chain until one answers. Auth failures never
// fall through: the token, not the host, is the problem.
func postCloudCode(ctx context.Context, d *Deps, auth *Auth, path string, payload any) ([]byte, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	var lastErr error
	for _, host := range cloudCodeHosts {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, host+path, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+auth.Token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "antigravity")
		for key, vals := range auth.Header {
			for _, v := range vals {
				req.Header.Add(key, v)
			}
		}

		resp, err := d.Client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			continue
		}

		switch {
		case resp.StatusCode >= 200 && resp.StatusCode < 300:
			return raw, nil
		case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
			d.FlushTokens()
			return nil, fmt.Errorf("%w: HTTP %d: %s", ErrExpiredAuth, resp.StatusCode, snippet(raw))
		case resp.StatusCode == http.StatusNotFound:
			// Endpoint absent on this host; try the next one.
			lastErr = fmt.Errorf("HTTP 404 on %s%s", host, path)
			continue
		default:
			lastErr = fmt.Errorf("HTTP %d on %s: %s", resp.StatusCode, host, snippet(raw))
		}
	}
	return nil, lastErr
}

// parseTime accepts the timestamp shapes the Cloud Code API emits.
func parseTime(raw string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05Z07:00"} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unparsable time %q", raw)
}

// displayName converts a model id into something readable when the API does not
// supply a name: gemini-3-pro-preview -> Gemini 3 Pro Preview.
func displayName(id, display string) string {
	if display != "" {
		return display
	}
	name := strings.TrimPrefix(id, "models/")
	parts := strings.Split(name, "-")
	for i, part := range parts {
		if part == "" {
			continue
		}
		if part[0] >= '0' && part[0] <= '9' {
			continue
		}
		parts[i] = strings.ToUpper(part[:1]) + part[1:]
	}
	return strings.Join(parts, " ")
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// sanitizeMetric keeps metric ids stable and log-safe.
// tierLabel translates Cloud Code tier ids into the names people recognise.
// Unknown ids fall back to the raw id.
func tierLabel(id string) string {
	switch strings.ToLower(id) {
	case "g1-ultra-lite-tier":
		return "Ultra 5x"
	case "g1-ultra-tier":
		return "Ultra 20x"
	case "g1-pro-tier":
		return "Pro"
	case "free-tier":
		return "Free"
	case "standard-tier":
		return "Standard"
	case "":
		return ""
	}
	return id
}

// familyOf classifies a model id. Internal catalogue entries return "".
func familyOf(metric string) string {
	l := strings.ToLower(metric)
	switch {
	case strings.HasPrefix(l, "chat_"), strings.HasPrefix(l, "tab"), strings.HasPrefix(l, "image_"):
		return ""
	case strings.HasPrefix(l, "gemini"):
		return "gemini"
	case strings.HasPrefix(l, "claude"), strings.HasPrefix(l, "gpt"):
		return "claude"
	}
	return "other"
}

// mergeFamilies turns per-model rows into one card per family. Models of a
// family share a pool, so the card carries the pool windows (5-hour, weekly)
// and shows the lowest reading among them.
func mergeFamilies(models []model.Snapshot, pools []poolSeries, credits []model.Series) []model.Snapshot {
	byFamily := map[string][]model.Snapshot{}
	var others []model.Snapshot
	for _, sn := range models {
		switch fam := familyOf(sn.Metric); fam {
		case "":
			continue
		case "other":
			others = append(others, sn)
		default:
			byFamily[fam] = append(byFamily[fam], sn)
		}
	}

	poolByFamily := map[string][]poolSeries{}
	for _, p := range pools {
		poolByFamily[p.family] = append(poolByFamily[p.family], p)
	}

	var out []model.Snapshot
	for _, fam := range []struct{ key, label string }{
		{"gemini", "Gemini models"},
		{"claude", "Claude & GPT models"},
	} {
		series := buildSeries(byFamily[fam.key], poolByFamily[fam.key])
		if len(series) == 0 {
			continue
		}
		out = append(out, familyCard(fam.key, fam.label, series, len(byFamily[fam.key])))
	}

	if card, ok := otherCard(credits, others); ok {
		out = append(out, card)
	}
	return out
}

// otherCard gathers everything the panel does not track closely: the two credit
// pools and the remaining catalogue models.
func otherCard(credits []model.Series, models []model.Snapshot) (model.Snapshot, bool) {
	series := append([]model.Series{}, credits...)
	if len(models) > 0 {
		lowest := models[0]
		for _, m := range models[1:] {
			if m.Remaining < lowest.Remaining {
				lowest = m
			}
		}
		label := "Other models"
		if len(models) > 1 {
			label = fmt.Sprintf("Other models (%d)", len(models))
		}
		series = append(series, model.Series{
			Label:     label,
			Remaining: lowest.Remaining,
			Total:     100,
			Unit:      "%",
			ResetAt:   lowest.ResetAt,
		})
	}
	if len(series) == 0 {
		return model.Snapshot{}, false
	}
	card := familyCard("other", "Other", series, 0)
	card.Status = model.StatusOK
	card.Note = ""
	return card, true
}

// familyCard builds a snapshot whose Series hold the windows, lowest first so
// the head-line number is the one that actually constrains the account.
func familyCard(key, label string, series []model.Series, modelCount int) model.Snapshot {
	lowest := series[0]
	for _, s := range series[1:] {
		if s.Remaining < lowest.Remaining {
			lowest = s
		}
	}
	note := ""
	if modelCount > 1 {
		note = fmt.Sprintf("%d models sharing one pool", modelCount)
	}
	return model.Snapshot{
		Provider:  "antigravity",
		Metric:    key + "_models",
		Label:     label,
		Remaining: lowest.Remaining,
		Total:     100,
		Unit:      "%",
		ResetAt:   lowest.ResetAt,
		Status:    statusFor(lowest.Remaining),
		Note:      note,
		Series:    series,
		UpdatedAt: time.Now().UTC(),
	}
}

// buildSeries prefers the pool windows; per-model values are the fallback when
// the account has no grouped summary.
func buildSeries(models []model.Snapshot, pools []poolSeries) []model.Series {
	var out []model.Series
	seen := map[string]bool{}
	for _, p := range pools {
		if seen[p.label] {
			continue
		}
		seen[p.label] = true
		out = append(out, model.Series{Label: p.label, Remaining: p.remaining, Total: 100, ResetAt: p.reset})
	}
	if len(out) > 0 {
		sort.SliceStable(out, func(i, j int) bool { return out[i].Label == "5-hour" && out[j].Label != "5-hour" })
		return out
	}

	if len(models) == 0 {
		return nil
	}
	lowest := models[0]
	for _, m := range models[1:] {
		if m.Remaining < lowest.Remaining {
			lowest = m
		}
	}
	label := "5-hour"
	if len(models) == 1 {
		label = "current"
	}
	return []model.Series{{Label: label, Remaining: lowest.Remaining, Total: 100, ResetAt: lowest.ResetAt}}
}

func statusFor(remaining float64) string {
	if remaining <= 0 {
		return model.StatusWarning
	}
	return model.StatusOK
}

// userFacingModel drops internal catalogue entries (chat_*, tab_*, image tools).
func userFacingModel(id string) bool {
	return familyOf(id) != ""
}

func sanitizeMetric(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_' || r == ' ' || r == '.':
			b.WriteByte('_')
		}
	}
	out := strings.Trim(b.String(), "_")
	if out == "" {
		return "bucket"
	}
	return out
}
