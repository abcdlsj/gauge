package providers

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/abcdlsj/gauge/internal/model"
)

// OpenDesign reads the Design Plan of an Open Design account. The windows sit
// on the control plane and are read with the account's control key, not the
// model (runtime) key. Two requests: the active workspace, then its billing
// preflight:
//
//	{"balanceUsd":…,"codingPlan":{"windows":[
//	  {"durationSeconds":18000,"usedCredits":…,"limitCredits":…,"resetsAt":"…"},…]}}
type OpenDesign struct{}

const openDesignAPI = "https://amr-api.open-design.ai"

func (OpenDesign) Name() string            { return "opendesign" }
func (OpenDesign) Title() string           { return "Open Design" }
func (OpenDesign) Interval() time.Duration { return 5 * time.Minute }

func (OpenDesign) Collect(ctx context.Context, d *Deps) ([]model.Snapshot, error) {
	var out []model.Snapshot
	var firstErr error
	for _, slot := range InstanceSlots(ctx, d, "opendesign_control_key") {
		snaps, user, err := collectOpenDesign(ctx, d, slot)
		if err != nil {
			if slot == 1 {
				firstErr = err
			}
			continue
		}
		stampInstance(ctx, d, snaps, "opendesign", slot, user)
		out = append(out, snaps...)
	}
	if len(out) == 0 && firstErr != nil {
		return nil, firstErr
	}
	return out, nil
}

// collectOpenDesign reads one instance: its workspace, then the billing
// preflight. The user is the email the CLI stored, empty for a pasted key.
func collectOpenDesign(ctx context.Context, d *Deps, slot int) ([]model.Snapshot, string, error) {
	key, user, from := openDesignKey(ctx, d, slot)
	if key == "" {
		if slot > 1 {
			return nil, "", nil
		}
		return nil, "", fmt.Errorf("%w: sign in with the Open Design CLI or paste a control key", ErrNotConfigured)
	}
	api := d.SettingDefault(ctx, "opendesign_api_url", openDesignAPI)

	const fix = "paste a current control key in settings or sign in again with the Open Design CLI"
	body, err := getBearerJSON(ctx, d, api+"/api/v1/workspaces/current", key)
	if err != nil {
		return nil, "", keySourceHint(err, from, fix)
	}
	var ws struct {
		Active string `json:"activeWorkspaceId"`
		ID     string `json:"workspaceId"`
	}
	if err := json.Unmarshal(body, &ws); err != nil {
		return nil, "", fmt.Errorf("opendesign: decode workspace: %w", err)
	}
	id := cmp.Or(ws.Active, ws.ID)
	if id == "" {
		return nil, "", fmt.Errorf("opendesign: the account has no active workspace")
	}

	body, err = getBearerJSON(ctx, d, api+"/api/v1/workspaces/"+url.PathEscape(id)+"/billing/preflight", key)
	if err != nil {
		return nil, "", keySourceHint(err, from, fix)
	}
	snaps, err := parseOpenDesign(body)
	if err != nil {
		return nil, "", err
	}
	return snaps, user, nil
}

// openDesignKey prefers a pasted control key, then the Open Design CLI's
// sign-in in ~/.amr/config.json (profile "prod").
func openDesignKey(ctx context.Context, d *Deps, slot int) (key, user, from string) {
	if v := d.Setting(ctx, InstanceKey("opendesign_control_key", slot)); v != "" {
		return v, "", "the pasted control key"
	}
	if slot > 1 {
		return "", "", ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", ""
	}
	raw, err := os.ReadFile(filepath.Join(home, ".amr", "config.json"))
	if err != nil {
		return "", "", ""
	}
	var cfg struct {
		Profiles map[string]struct {
			ControlKey string `json:"controlKey"`
			User       struct {
				Email string `json:"email"`
			} `json:"user"`
		} `json:"profiles"`
	}
	if json.Unmarshal(raw, &cfg) != nil {
		return "", "", ""
	}
	p := cfg.Profiles["prod"]
	return p.ControlKey, p.User.Email, "~/.amr/config.json"
}

func parseOpenDesign(body []byte) ([]model.Snapshot, error) {
	var pre struct {
		Balance    *flexFloat `json:"balanceUsd"`
		CodingPlan struct {
			Windows []struct {
				Duration flexFloat `json:"durationSeconds"`
				Used     flexFloat `json:"usedCredits"`
				Limit    flexFloat `json:"limitCredits"`
				Resets   string    `json:"resetsAt"`
			} `json:"windows"`
		} `json:"codingPlan"`
	}
	if err := json.Unmarshal(body, &pre); err != nil {
		return nil, fmt.Errorf("opendesign: decode preflight: %w", err)
	}

	var series []model.Series
	for _, w := range pre.CodingPlan.Windows {
		if w.Limit <= 0 {
			continue
		}
		s := model.Series{
			Label:     spanLabel(time.Duration(w.Duration) * time.Second),
			Remaining: percentLeft(float64(w.Used), float64(w.Limit)),
			Total:     100,
			Unit:      "%",
		}
		if t, err := time.Parse(time.RFC3339, w.Resets); err == nil {
			t = t.UTC()
			s.ResetAt = &t
		}
		series = append(series, s)
	}

	var out []model.Snapshot
	if len(series) > 0 {
		out = append(out, planSnapshot("opendesign", "Design Plan", series))
	}
	if pre.Balance != nil {
		out = append(out, model.Snapshot{
			Provider: "opendesign", Metric: "balance", Label: "Balance",
			Remaining: float64(*pre.Balance), Unit: "USD", Status: model.StatusOK,
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("opendesign: the workspace reports no plan windows or balance")
	}
	return out, nil
}

// spanLabel names a window by its length: 5-hour, 7-day.
func spanLabel(d time.Duration) string {
	switch {
	case d <= 0:
		return "Window"
	case d == 7*24*time.Hour:
		return "Weekly"
	case d%(24*time.Hour) == 0:
		return fmt.Sprintf("%d-day", d/(24*time.Hour))
	case d%time.Hour == 0:
		return fmt.Sprintf("%d-hour", d/time.Hour)
	}
	return fmt.Sprintf("%d-min", d/time.Minute)
}
