package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/abcdlsj/gauge/internal/model"
)

// Antigravity collects model quota for up to three Google accounts. Account 1
// is read from the running IDE's language server (antigravity_local.go); the
// Cloud Code private API below is its fallback and the only source for
// accounts 2 and 3, which exist through Gauge's own sign-in.
//
// Findings that shape this file (verified against AntigravityqQuotaWatcher,
// Antigravity-Manager and antigravity-panel):
//   - the quota endpoint only accepts tokens minted for the Antigravity OAuth
//     client; tokens from other Google clients are rejected with PERMISSION_DENIED
//   - the API lives on three hosts, daily first, then the sandbox, then prod
//   - loadCodeAssist with metadata.ideType=ANTIGRAVITY reveals the subscription
//     tier and the cloudaicompanion project
//   - retrieveUserQuotaSummary exposes the grouped 5h / weekly quota pools, but
//     Google withholds groups from clients it does not recognise: the bare
//     "antigravity" identity gets the third-party group alone, a versioned
//     identity also gets the Gemini group, and only the hub/cli identity gets
//     the Gemini 4 Argon group (a separate weekly-only allowance on Pro plans)
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

// Sources recorded under antigravity_source for slot 1.
const (
	sourceLocalIDE   = "local_ide"   // the running IDE's language server
	sourceCloudAPI   = "cloud_api"   // the Cloud Code API with Gauge's sign-in
	sourceIDEOffline = "ide_offline" // IDE closed and no Gauge sign-in
)

// Collect reads slot 1 from the running IDE whenever it answers; the Cloud Code
// API is the fallback for that account. Slots 2 and 3 exist only through
// Gauge's sign-in, so they always use the Cloud Code API.
func (Antigravity) Collect(ctx context.Context, d *Deps) ([]model.Snapshot, error) {
	local, ideUp := collectLocal(ctx, d)
	if ideUp && local.account.Email != "" {
		d.rememberIDEAccount(ctx, local.account)
		// The IDE is one channel of an account, not the slot it lives in: a
		// sign-in left behind on slot 1 moves out of the way when the IDE
		// starts using a different account.
		d.promoteSignIn(ctx, local.account.Email)
	}

	primary, snaps, err := collectPrimary(ctx, d, local, ideUp)
	seen := ""
	if err == nil && len(snaps) > 0 {
		seen = primary.Email
	}
	extra, extraAccounts := collectExtraAccounts(ctx, d, seen)

	// The IDE being closed is not news when its last account still has a
	// sign-in of its own that answered: that slot already shows it, so a second
	// row would report the same account twice. A silent slot 1 would instead
	// leave its last readings looking current, so a failed read keeps the error.
	if err != nil && len(extra) > 0 && shownElsewhere(extraAccounts, primary.Email) {
		d.Log.Printf("antigravity: account 1: %v; %s already has its own sign-in", err, primary.Email)
		d.SetAccounts(ctx, extraAccounts)
		return extra, nil
	}

	accounts := []model.Account{}
	if primary.Email != "" {
		accounts = append(accounts, primary)
	}
	d.SetAccounts(ctx, append(accounts, extraAccounts...))

	if err != nil {
		if len(extra) == 0 {
			// The engine leaves stored readings alone for an unconfigured
			// source; flag them so they do not pass for current numbers.
			if errors.Is(err, ErrNotConfigured) {
				_ = d.Store.MarkProviderStatus(ctx, "antigravity", model.StatusError)
			}
			return nil, err
		}
		// The other accounts answered, and their readings replace the stored
		// set: carry slot 1's last readings over, flagged, so its account
		// stays on screen instead of vanishing.
		d.Log.Printf("antigravity: account 1: %v", err)
		snaps = d.staleSlotOne(ctx)
	}
	return append(snaps, extra...), nil
}

// collectPrimary reads slot 1: the IDE first, then Gauge's own sign-in.
func collectPrimary(ctx context.Context, d *Deps, local localReading, ideUp bool) (model.Account, []model.Snapshot, error) {
	signedIn := d.hasSignIn(ctx, 1)
	var (
		who     model.Account
		expired bool
	)
	if signedIn {
		who, expired = d.signInIdentity(ctx, 1)
	}
	// stamp records both identities on whichever account slot 1 shows.
	stamp := func(a model.Account, ideEmail string) model.Account {
		a.Slot, a.Primary, a.OAuth = 1, true, signedIn
		a.IDEEmail = ideEmail
		a.AuthExpired = expired
		if signedIn {
			a.SignInEmail = who.Email
		}
		return a
	}

	if ideUp {
		account := stamp(local.account, local.account.Email)
		if len(local.pools) > 0 {
			_ = d.Store.Set(ctx, "antigravity_source", sourceLocalIDE)
			return account, mergeFamilies(nil, local.pools, local.credits), nil
		}
		// The IDE answered without quota pools. Gauge's sign-in may stand in
		// only when it is the same account; otherwise its numbers would sit
		// under the IDE's name.
		sameAccount := signedIn && who.Email != "" && strings.EqualFold(who.Email, account.Email)
		if sameAccount {
			models, pools, _, err := collectRemote(ctx, d, local.labels, SourceAntigravity)
			if err == nil {
				_ = d.Store.Set(ctx, "antigravity_source", sourceCloudAPI)
				return account, mergeFamilies(models, pools, local.credits), nil
			}
			account.AuthExpired = errors.Is(err, ErrExpiredAuth)
			d.Log.Printf("antigravity: account 1: Cloud Code fallback failed: %v", err)
		} else if signedIn {
			d.Log.Printf("antigravity: account 1: Gauge is signed in as %q, the IDE as %q; not mixing them",
				who.Email, account.Email)
		}
		_ = d.Store.Set(ctx, "antigravity_source", sourceLocalIDE)
		if card, ok := otherCard(local.credits, nil); ok {
			return account, []model.Snapshot{card}, nil
		}
		return account, nil, fmt.Errorf("the IDE returned no quota pools for %s", account.Email)
	}

	// The IDE is closed.
	last := d.lastIDEAccount(ctx)
	if !signedIn {
		_ = d.Store.Set(ctx, "antigravity_source", sourceIDEOffline)
		return stamp(last, last.Email), nil, fmt.Errorf("%w: %s", ErrNotConfigured, IDEOfflineHint)
	}
	// Gauge's sign-in names its own account, which need not be the one the IDE
	// last used; with no answer from userinfo it stays unnamed.
	account := stamp(who, last.Email)
	_ = d.Store.Set(ctx, "antigravity_source", sourceCloudAPI)
	models, pools, info, err := collectRemote(ctx, d, nil, SourceAntigravity)
	if err != nil {
		account.AuthExpired = errors.Is(err, ErrExpiredAuth)
		return account, nil, err
	}
	account.TierID = tierName(info)
	account.Tier = tierLabel(account.TierID)
	return account, mergeFamilies(models, pools, nil), nil
}

// IDEOfflineHint is the error slot 1 reports while the IDE is closed and Gauge
// holds no sign-in.
const IDEOfflineHint = "the Antigravity IDE is not running; open it, or sign in to Gauge with this account to read its quota while the IDE is closed"

// staleSlotOne returns slot 1's stored readings marked as failed.
func (d *Deps) staleSlotOne(ctx context.Context) []model.Snapshot {
	all, err := d.Store.Snapshots(ctx)
	if err != nil {
		return nil
	}
	var out []model.Snapshot
	for _, sn := range all {
		if sn.Provider != "antigravity" || strings.Contains(sn.Metric, "@") {
			continue
		}
		sn.Status = model.StatusError
		out = append(out, sn)
	}
	return out
}

// maxAccounts bounds how many identities the panel will poll.
const maxAccounts = 3

// collectExtraAccounts reads slots 2 and 3 through Gauge's sign-in. An account
// already shown under an earlier slot is listed but not read again; seen is
// the email slot 1 was read for, empty when slot 1 has no reading.
func collectExtraAccounts(ctx context.Context, d *Deps, seen string) ([]model.Snapshot, []model.Account) {
	var (
		snaps    []model.Snapshot
		accounts []model.Account
	)
	shown := map[string]int{}
	if seen != "" {
		shown[strings.ToLower(seen)] = 1
	}
	for slot := 2; slot <= maxAccounts; slot++ {
		if !d.hasSignIn(ctx, slot) {
			continue
		}
		label, expired := d.signInIdentity(ctx, slot)
		label.Slot, label.OAuth, label.AuthExpired = slot, true, expired
		if prev, dup := shown[strings.ToLower(label.Email)]; dup && label.Email != "" {
			label.DuplicateOf = prev
			d.Log.Printf("antigravity: account %d is %s, already shown as account %d; skipping", slot, label.Email, prev)
			accounts = append(accounts, label)
			continue
		}
		if label.Email != "" {
			shown[strings.ToLower(label.Email)] = slot
		}

		models, pools, info, err := collectRemote(ctx, d, nil, sourceFor(slot))
		if err != nil {
			label.AuthExpired = errors.Is(err, ErrExpiredAuth)
			d.Log.Printf("antigravity: account %d: %v", slot, err)
			accounts = append(accounts, label)
			continue
		}
		for _, sn := range mergeFamilies(models, pools, nil) {
			sn.Metric = fmt.Sprintf("%s@%d", sn.Metric, slot)
			sn.Account = label.Email
			snaps = append(snaps, sn)
		}
		label.TierID = tierName(info)
		label.Tier = tierLabel(label.TierID)
		accounts = append(accounts, label)
	}
	return snaps, accounts
}

// collectRemote reads the Cloud Code quota API. labels maps the IDE's model ids
// to display names.
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

// antigravityHubUserAgent is the identity the IDE's own hub client presents.
// Google gates quota groups on it: the Gemini group needs a versioned identity,
// the Argon group needs the hub/cli shape. Without it a Pro account is told it
// has no Gemini pool at all.
func antigravityHubUserAgent() string {
	return fmt.Sprintf("antigravity/hub/%s (aidev_client; os_type=%s; arch=%s)",
		hubClientVersion(), uaOS(), uaArch())
}

// hubClientFallbackVersion is used while no IDE discovery file names one. It is
// the hub release Gauge was last checked against; the version cache is the
// live source.
const hubClientFallbackVersion = "2.33.0"

func uaOS() string {
	switch runtime.GOOS {
	case "darwin", "windows", "linux":
		return runtime.GOOS
	}
	return "linux"
}

func uaArch() string {
	switch runtime.GOARCH {
	case "arm64", "amd64":
		return runtime.GOARCH
	case "386":
		return "386"
	}
	return "amd64"
}

// quotaSummary returns the grouped 5-hour / weekly pools. Best effort: older
// accounts get 404 here and that is fine.
func quotaSummary(ctx context.Context, d *Deps, auth *Auth) []poolSeries {
	body, err := postCloudCodeAs(ctx, d, auth, antigravityHubUserAgent(), "/v1internal:retrieveUserQuotaSummary", map[string]any{})
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

// groupFamily maps a quota-summary group name onto a model family. Argon is
// listed before Gemini: a Pro plan's Argon allowance is its own weekly-only
// bucket, not part of the Gemini pool.
func groupFamily(name string) string {
	l := strings.ToLower(name)
	switch {
	case strings.Contains(l, "claude") || strings.Contains(l, "gpt"):
		return "claude"
	case strings.Contains(l, "argon"):
		return "argon"
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

// postCloudCode walks the host chain until one answers as the bare client
// identity. Model requests keep it; only the quota summary needs the hub one.
func postCloudCode(ctx context.Context, d *Deps, auth *Auth, path string, payload any) ([]byte, error) {
	return postCloudCodeAs(ctx, d, auth, "antigravity", path, payload)
}

// postCloudCodeAs walks the host chain until one answers. Auth failures never
// fall through: the token, not the host, is the problem.
func postCloudCodeAs(ctx context.Context, d *Deps, auth *Auth, userAgent, path string, payload any) ([]byte, error) {
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
		req.Header.Set("User-Agent", userAgent)
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
		case resp.StatusCode == http.StatusTooManyRequests:
			return nil, fmt.Errorf("%w: HTTP 429 on %s: %s", ErrRateLimited, host, snippet(raw))
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
		card := familyCard(fam.key, fam.label, series, len(byFamily[fam.key]))
		if fam.key == "gemini" {
			// Argon's allowance is drawn separately from the Gemini pool, so it
			// rides along as its own row and does not pull the card down.
			card.Series = append(card.Series, dedicatedSeries("Argon", poolByFamily["argon"])...)
		}
		out = append(out, card)
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

// dedicatedSeries renders a pool one model family owns on its own, keeping its
// window label readable next to the shared ones.
func dedicatedSeries(name string, pools []poolSeries) []model.Series {
	out := make([]model.Series, 0, len(pools))
	for _, p := range pools {
		out = append(out, model.Series{
			Label:     name + " · " + p.label,
			Remaining: p.remaining,
			Total:     100,
			ResetAt:   p.reset,
		})
	}
	return out
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
