package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/abcdlsj/gauge/internal/model"
)

// GoogleFlow reads the video-credit balance of flow.google.com.
//
// Same transport as Colab: a gRPC-Web endpoint on clients6.google.com, called
// with a cookie-derived SAPISIDHASH header. The Flow frontend ships its own API
// key; without it the API answers 403 "blocked".
type GoogleFlow struct{}

const (
	defaultFlowEndpoint = "https://aisandbox-pa.clients6.google.com/$rpc/google.internal.labs.aisandbox.proto.videofx.v1.VideoFxService/GetCredits"
	flowOrigin          = "https://flow.google.com"
	flowAPIKey          = "AIzaSyBAJFcGewaFGLzed-O3e9LjJlUUWTbp83Q"
)

func (GoogleFlow) Name() string            { return "google-flow" }
func (GoogleFlow) Title() string           { return "Google Flow" }
func (GoogleFlow) Interval() time.Duration { return 30 * time.Minute }

// Collect fetches the credit balance. Colab's session cookies are reused: both
// products live under the same Google account.
func (GoogleFlow) Collect(ctx context.Context, d *Deps) ([]model.Snapshot, error) {
	endpoint := d.SettingDefault(ctx, "flow_endpoint", defaultFlowEndpoint)
	apiKey := d.SettingDefault(ctx, "flow_api_key", flowAPIKey)
	cookie := flowCookie(ctx, d)
	if cookie == "" {
		return nil, fmt.Errorf("%w: no cookie; enable browser cookies or paste flow_cookie", ErrNotConfigured)
	}

	// A rejected session pauses polling until a new cookie appears.
	if d.Setting(ctx, "flow_auth_state") == "expired" {
		if d.Setting(ctx, "flow_cookie_fp") == shortFingerprint(cookie) {
			return nil, fmt.Errorf("%w: session paused, update the cookie", ErrExpiredAuth)
		}
		_ = d.Store.Delete(ctx, "flow_auth_state")
	}

	auth, err := sapisidAuth(cookie, flowOrigin)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrExpiredAuth, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader("[null,1]"))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json+protobuf")
	req.Header.Set("Authorization", auth)
	req.Header.Set("Origin", flowOrigin)
	req.Header.Set("Referer", flowOrigin+"/")
	req.Header.Set("X-Goog-Api-Key", apiKey)
	req.Header.Set("X-Goog-AuthUser", "0")
	req.Header.Set("X-User-Agent", "grpc-web-javascript/0.1")
	req.Header.Set("Cookie", cookie)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/142.0 Safari/537.36")

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
	case http.StatusUnauthorized, http.StatusForbidden:
		_ = d.Store.Set(ctx, "flow_auth_state", "expired")
		_ = d.Store.Set(ctx, "flow_cookie_fp", shortFingerprint(cookie))
		return nil, fmt.Errorf("%w: HTTP %d: %s", ErrExpiredAuth, resp.StatusCode, snippet(body))
	case http.StatusTooManyRequests:
		return nil, fmt.Errorf("%w: HTTP 429: %s", ErrRateLimited, snippet(body))
	default:
		return nil, fmt.Errorf("flow: HTTP %d: %s", resp.StatusCode, snippet(body))
	}

	balance, err := parseFlowCredits(body)
	if err != nil {
		return nil, err
	}
	status := model.StatusOK
	if balance.Total > 0 && balance.Remaining <= balance.Total*0.05 {
		status = model.StatusWarning
	}

	return []model.Snapshot{{
		Provider:  "google-flow",
		Metric:    "flow_credits",
		Label:     "Video credits",
		Remaining: balance.Remaining,
		Total:     balance.Total,
		Unit:      "credits",
		ResetAt:   balance.ResetAt,
		Status:    status,
		UpdatedAt: time.Now().UTC(),
	}}, nil
}

// flowBalance is the decoded GetCredits reply.
type flowBalance struct {
	Total     float64
	Remaining float64
	ResetAt   *time.Time
}

// parseFlowCredits decodes the positional protobuf reply:
//
//	[total, …, remaining, ["<unix>", <nanos>]]
func parseFlowCredits(body []byte) (flowBalance, error) {
	var fields []json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return flowBalance{}, fmt.Errorf("flow: decode: %w", err)
	}
	if len(fields) < 2 {
		return flowBalance{}, fmt.Errorf("flow: unexpected reply with %d fields", len(fields))
	}

	var out flowBalance
	_ = json.Unmarshal(fields[0], &out.Total)
	// The remaining balance sits after the counters; fall back to the total so a
	// layout change cannot produce a bogus zero.
	remainingIndex := len(fields) - 2
	if remainingIndex < 1 {
		remainingIndex = 1
	}
	_ = json.Unmarshal(fields[remainingIndex], &out.Remaining)
	if out.Remaining == 0 && out.Total > 0 {
		out.Remaining = out.Total
	}

	if len(fields) > 0 {
		var reset []json.RawMessage
		if json.Unmarshal(fields[len(fields)-1], &reset) == nil && len(reset) > 0 {
			var ts string
			if json.Unmarshal(reset[0], &ts) == nil {
				if seconds, err := strconv.ParseInt(ts, 10, 64); err == nil {
					t := time.Unix(seconds, 0).UTC()
					out.ResetAt = &t
				}
			}
		}
	}
	return out, nil
}

// flowCookie prefers the live browser session over a pasted cookie.
func flowCookie(ctx context.Context, d *Deps) string {
	if header, ok := d.BrowserCookies(ctx, "google.com", googleCookieNames); ok {
		return header
	}
	return d.Setting(ctx, "flow_cookie")
}
