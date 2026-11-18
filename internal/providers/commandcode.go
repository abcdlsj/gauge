package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/abcdlsj/gauge/internal/model"
)

// CommandCode reads the plan windows of a Command Code subscription.
//
//	GET https://api.commandcode.ai/alpha/billing/credits
//	{"credits":{"monthlyCredits":…,"purchasedCredits":…},
//	 "windowLimits":{"fiveHour":{"used":…,"cap":…,"resetAt":<ms>},
//	                 "weekly":{"used":…,"cap":…,"resetAt":<ms>}}}
//
// A resetAt of 0 means the window has not started yet.
type CommandCode struct{}

const commandCodeCreditsURL = "https://api.commandcode.ai/alpha/billing/credits"

func (CommandCode) Name() string            { return "commandcode" }
func (CommandCode) Title() string           { return "Command Code" }
func (CommandCode) Interval() time.Duration { return 5 * time.Minute }

func (CommandCode) Collect(ctx context.Context, d *Deps) ([]model.Snapshot, error) {
	key, user := commandCodeKey(ctx, d)
	if key == "" {
		return nil, fmt.Errorf("%w: sign in with the Command Code CLI or paste an API key", ErrNotConfigured)
	}
	body, err := getBearerJSON(ctx, d, d.SettingDefault(ctx, "commandcode_endpoint", commandCodeCreditsURL), key)
	if err != nil {
		return nil, err
	}
	snaps, err := parseCommandCode(body)
	if err != nil {
		return nil, err
	}
	for i := range snaps {
		snaps[i].Account = user
	}
	return snaps, nil
}

// commandCodeKey prefers a pasted key, then the Command Code CLI's own sign-in.
func commandCodeKey(ctx context.Context, d *Deps) (key, user string) {
	if v := d.Setting(ctx, "commandcode_api_key"); v != "" {
		return v, ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", ""
	}
	raw, err := os.ReadFile(filepath.Join(home, ".commandcode", "auth.json"))
	if err != nil {
		return "", ""
	}
	var auth struct {
		APIKey   string `json:"apiKey"`
		UserName string `json:"userName"`
	}
	if json.Unmarshal(raw, &auth) != nil {
		return "", ""
	}
	return auth.APIKey, auth.UserName
}

type ccWindow struct {
	Used    float64 `json:"used"`
	Cap     float64 `json:"cap"`
	ResetAt float64 `json:"resetAt"`
}

func parseCommandCode(body []byte) ([]model.Snapshot, error) {
	var reply struct {
		Credits struct {
			Monthly   *float64 `json:"monthlyCredits"`
			Purchased *float64 `json:"purchasedCredits"`
		} `json:"credits"`
		Windows struct {
			FiveHour *ccWindow `json:"fiveHour"`
			Weekly   *ccWindow `json:"weekly"`
		} `json:"windowLimits"`
	}
	if err := json.Unmarshal(body, &reply); err != nil {
		return nil, fmt.Errorf("commandcode: decode: %w", err)
	}

	var series []model.Series
	for _, w := range []struct {
		label string
		win   *ccWindow
	}{{"5-hour", reply.Windows.FiveHour}, {"Weekly", reply.Windows.Weekly}} {
		if w.win == nil || w.win.Cap <= 0 {
			continue
		}
		s := model.Series{Label: w.label, Remaining: percentLeft(w.win.Used, w.win.Cap), Total: 100, Unit: "%"}
		if w.win.ResetAt > 0 {
			t := time.UnixMilli(int64(w.win.ResetAt)).UTC()
			s.ResetAt = &t
		}
		series = append(series, s)
	}

	var out []model.Snapshot
	if len(series) > 0 {
		out = append(out, planSnapshot("commandcode", "Plan", series))
	}
	if reply.Credits.Monthly != nil || reply.Credits.Purchased != nil {
		var total float64
		if reply.Credits.Monthly != nil {
			total += *reply.Credits.Monthly
		}
		if reply.Credits.Purchased != nil {
			total += *reply.Credits.Purchased
		}
		out = append(out, model.Snapshot{
			Provider: "commandcode", Metric: "credits", Label: "Credits",
			Remaining: total, Unit: "credits", Status: model.StatusOK,
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("commandcode: the key has no plan windows (pay-as-you-go keys are not supported)")
	}
	return out, nil
}

// percentLeft turns used/limit into the remaining percentage, clamped to 0–100.
func percentLeft(used, limit float64) float64 {
	if limit <= 0 {
		return 0
	}
	return max(0, min(100, 100-100*used/limit))
}

// planSnapshot folds quota windows into one card; the tightest window decides
// the headline number and the status.
func planSnapshot(provider, label string, series []model.Series) model.Snapshot {
	low := series[0]
	for _, s := range series[1:] {
		if s.Remaining < low.Remaining {
			low = s
		}
	}
	status := model.StatusOK
	if low.Remaining <= 5 {
		status = model.StatusWarning
	}
	return model.Snapshot{
		Provider: provider, Metric: "plan", Label: label,
		Remaining: low.Remaining, Total: 100, Unit: "%",
		Series: series, ResetAt: low.ResetAt, Status: status,
	}
}

// getBearerJSON sends one authenticated GET and returns the 200 body. 401 and
// 403 map onto ErrExpiredAuth so the UI asks for a new credential.
func getBearerJSON(ctx context.Context, d *Deps, url, key string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "gauge/1")
	resp, err := d.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := readLimited(resp.Body, 1<<20)
	if err != nil {
		return nil, err
	}
	switch resp.StatusCode {
	case http.StatusOK:
		return body, nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, fmt.Errorf("%w: HTTP %d: %s", ErrExpiredAuth, resp.StatusCode, snippet(body))
	default:
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, snippet(body))
	}
}
