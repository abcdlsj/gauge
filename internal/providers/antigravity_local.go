package providers

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/abcdlsj/gauge/internal/model"
)

// Local Antigravity support.
//
// The IDE ships a language server that already knows everything the panel needs:
// plan, prompt/flow credits and the model catalogue. It listens on loopback and
// authenticates with a CSRF token, which it writes to
// <gemini_root>/antigravity/daemon/hub_<port>.json together with its pid and
// ports. Reading that file avoids OAuth entirely and is what antigravity-panel
// and AntigravityQuotaWatcher do.

// CONNECT endpoints on the local language server.
const (
	languageServerRPC = "/exa.language_server_pb.LanguageServerService/GetUserStatus"
	quotaSummaryRPC   = "/exa.language_server_pb.LanguageServerService/RetrieveUserQuotaSummary"
)

// hubRoots lists the data directories the IDE uses, newest layout first.
var hubRoots = []string{
	"~/.gemini/antigravity/daemon",
	"~/.gemini/antigravity-ide/daemon",
	"~/.antigravity/daemon",
	"~/.config/antigravity/daemon",
}

// hubDiscovery mirrors daemon/hub_<port>.json.
type hubDiscovery struct {
	PID       int    `json:"pid"`
	HTTPSPort int    `json:"httpsPort"`
	HTTPPort  int    `json:"httpPort"`
	Version   string `json:"lsVersion"`
	CSRFToken string `json:"csrfToken"`
	path      string
	modified  time.Time
}

// userStatus is the subset of GetUserStatus the panel consumes.
type userStatus struct {
	Name       string `json:"name"`
	Email      string `json:"email"`
	PlanStatus struct {
		PlanInfo struct {
			PlanName             string  `json:"planName"`
			TeamsTier            string  `json:"teamsTier"`
			MonthlyPromptCredits float64 `json:"monthlyPromptCredits"`
			MonthlyFlowCredits   float64 `json:"monthlyFlowCredits"`
		} `json:"planInfo"`
		AvailablePromptCredits float64 `json:"availablePromptCredits"`
		AvailableFlowCredits   float64 `json:"availableFlowCredits"`
	} `json:"planStatus"`
	Tier struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"userTier"`
	Picture string `json:"profilePictureUrl"`
	Cascade struct {
		Configs []struct {
			ModelID string `json:"modelId"`
			Label   string `json:"label"`
		} `json:"clientModelConfigs"`
	} `json:"cascadeModelConfigData"`
}

// localReading is what the running IDE reported for the account it is signed
// in to.
type localReading struct {
	account model.Account
	// labels maps model ids to the names the IDE shows.
	labels  map[string]string
	credits []model.Series
	pools   []poolSeries
}

// collectLocal reads the identity, credits and quota pools from the running
// IDE. ok is false when no IDE answered.
func collectLocal(ctx context.Context, d *Deps) (localReading, bool) {
	var out localReading
	hub, err := discoverHub()
	if err != nil {
		d.Log.Printf("antigravity: local IDE unavailable: %v", err)
		return out, false
	}

	status, err := fetchLocalStatus(ctx, d, hub)
	if err != nil {
		d.Log.Printf("antigravity: local IDE unavailable: %v", err)
		return out, false
	}
	out.pools = fetchLocalPools(ctx, d, hub)

	out.account = model.Account{
		Slot:    1,
		Email:   status.Email,
		Name:    status.Name,
		Tier:    firstNonEmpty(tierLabel(status.Tier.ID), status.Tier.Name),
		TierID:  status.Tier.ID,
		Picture: status.Picture,
		Primary: true,
	}

	out.labels = make(map[string]string, len(status.Cascade.Configs))
	for _, cfg := range status.Cascade.Configs {
		if cfg.ModelID != "" && cfg.Label != "" {
			out.labels[cfg.ModelID] = cfg.Label
		}
	}

	plan := status.PlanStatus.PlanInfo
	if plan.MonthlyPromptCredits > 0 {
		out.credits = append(out.credits, creditSeries("Prompt credits",
			status.PlanStatus.AvailablePromptCredits, plan.MonthlyPromptCredits))
	}
	if plan.MonthlyFlowCredits > 0 {
		out.credits = append(out.credits, creditSeries("Flow credits",
			status.PlanStatus.AvailableFlowCredits, plan.MonthlyFlowCredits))
	}
	return out, true
}

// creditSeries renders one credit pool as a series row.
func creditSeries(label string, available, monthly float64) model.Series {
	return model.Series{Label: label, Remaining: available, Total: monthly, Unit: "credits"}
}

// fetchLocalStatus calls GetUserStatus on a discovered language server.
func fetchLocalStatus(ctx context.Context, d *Deps, hub hubDiscovery) (userStatus, error) {
	var out userStatus
	body := map[string]any{"metadata": map[string]string{
		"ideName":       "antigravity",
		"extensionName": "antigravity",
		"locale":        "en",
	}}
	raw, err := callLanguageServer(ctx, d, hub, languageServerRPC, body)
	if err != nil {
		return out, err
	}
	var envelope struct {
		UserStatus userStatus `json:"userStatus"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return out, fmt.Errorf("decode user status: %w", err)
	}
	return envelope.UserStatus, nil
}

// discoverHub finds the newest hub_*.json whose process is still alive.
func discoverHub() (hubDiscovery, error) {
	var candidates []hubDiscovery
	for _, root := range hubRoots {
		dir := expandHome(root)
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			name := entry.Name()
			if !strings.HasPrefix(name, "hub_") || !strings.HasSuffix(name, ".json") {
				continue
			}
			path := filepath.Join(dir, name)
			raw, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			var hub hubDiscovery
			if err := json.Unmarshal(raw, &hub); err != nil {
				continue
			}
			if hub.CSRFToken == "" || (hub.HTTPPort == 0 && hub.HTTPSPort == 0) {
				continue
			}
			if info, err := entry.Info(); err == nil {
				hub.modified = info.ModTime()
			}
			hub.path = path
			candidates = append(candidates, hub)
		}
	}
	if len(candidates) == 0 {
		return hubDiscovery{}, fmt.Errorf("no IDE discovery file found")
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].modified.After(candidates[j].modified) })
	for _, hub := range candidates {
		if processAlive(hub.PID) {
			return hub, nil
		}
	}
	return hubDiscovery{}, fmt.Errorf("no running IDE process found")
}

// localTLSClient accepts the language server's self-signed certificate. It only
// ever talks to 127.0.0.1 and is reused across calls to avoid leaking transports.
var localTLSClient = &http.Client{
	Timeout: 5 * time.Second,
	Transport: &http.Transport{
		Proxy:           nil,
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	},
}

// fetchLocalPools asks the IDE for the grouped quota summary. This is the only
// source that reports the weekly window for Gemini: the public REST variant
// returns the third-party group only.
func fetchLocalPools(ctx context.Context, d *Deps, hub hubDiscovery) []poolSeries {
	body, err := callLanguageServer(ctx, d, hub, quotaSummaryRPC, map[string]any{})
	if err != nil {
		d.Log.Printf("antigravity: local quota summary unavailable: %v", err)
		return nil
	}

	var resp struct {
		Response struct {
			Groups []struct {
				DisplayName string `json:"displayName"`
				Buckets     []struct {
					Window            string   `json:"window"`
					RemainingFraction *float64 `json:"remainingFraction"`
					ResetTime         string   `json:"resetTime"`
				} `json:"buckets"`
			} `json:"groups"`
		} `json:"response"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		d.Log.Printf("antigravity: decode local quota summary: %v", err)
		return nil
	}

	var out []poolSeries
	for _, g := range resp.Response.Groups {
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

// callLanguageServer posts a JSON body to a CONNECT endpoint on the IDE.
func callLanguageServer(ctx context.Context, d *Deps, hub hubDiscovery, path string, payload any) ([]byte, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	// Plain HTTP first; the IDE also exposes TLS with a self-signed cert.
	for _, attempt := range []struct {
		scheme string
		port   int
		client *http.Client
	}{
		{"http", hub.HTTPPort, d.Client},
		{"https", hub.HTTPSPort, localTLSClient},
	} {
		if attempt.port <= 0 {
			continue
		}
		reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		url := fmt.Sprintf("%s://127.0.0.1:%d%s", attempt.scheme, attempt.port, path)
		req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			cancel()
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Connect-Protocol-Version", "1")
		req.Header.Set("X-Codeium-Csrf-Token", hub.CSRFToken)

		resp, err := attempt.client.Do(req)
		if err != nil {
			cancel()
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue
		}
		raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		resp.Body.Close()
		cancel()
		if readErr != nil || resp.StatusCode != http.StatusOK {
			continue
		}
		return raw, nil
	}
	return nil, fmt.Errorf("language server on pid %d did not answer %s", hub.PID, path)
}
