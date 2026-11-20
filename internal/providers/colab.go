package providers

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/abcdlsj/gauge/internal/model"
)

// Colab reads the compute-unit balance from Colab's own gRPC-Web endpoint.
//
// The browser authenticates with SAPISIDHASH: SHA1("<ts> <SAPISID> <origin>")
// derived from the Google session cookies. Cookie values are taken from the
// local Chrome profile when available, so the panel always uses the live
// session and never stores session material.
type Colab struct{}

// Defaults copied from Colab's own frontend request.
const (
	defaultColabEndpoint = "https://colab.clients6.google.com/$rpc/google.internal.colab.v1.UserInfoService/GetUserInfo"
	colabOrigin          = "https://colab.research.google.com"
	colabAPIKey          = "AIzaSyA2BvntLwNwFthUB4w6_Bhn0cMlVHwyaHc"
)

func (Colab) Name() string            { return "colab" }
func (Colab) Title() string           { return "Colab" }
func (Colab) Interval() time.Duration { return 30 * time.Minute }

// collectResponse is the positional protobuf-JSON reply:
//
//	[status, computeUnits, accelerators, ..., ratio, tier]
//
// Only the fields the panel uses are decoded.
type collectResponse []json.RawMessage

// Collect fetches the balance and names the accelerator classes available today.
func (Colab) Collect(ctx context.Context, d *Deps) ([]model.Snapshot, error) {
	endpoint := d.SettingDefault(ctx, "colab_endpoint", defaultColabEndpoint)
	cookie := colabCookie(ctx, d)
	if cookie == "" {
		return nil, fmt.Errorf("%w: no cookie; enable browser cookies or paste colab_cookie", ErrNotConfigured)
	}

	// A rejected session pauses polling until a new cookie appears.
	if d.Setting(ctx, "colab_auth_state") == "expired" {
		if d.Setting(ctx, "colab_cookie_fp") == shortFingerprint(cookie) {
			return nil, fmt.Errorf("%w: session paused, update the cookie", ErrExpiredAuth)
		}
		_ = d.Store.Delete(ctx, "colab_auth_state")
	}

	auth, err := sapisidAuth(cookie, colabOrigin)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrExpiredAuth, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader("[null,1]"))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json+protobuf")
	req.Header.Set("Authorization", auth)
	req.Header.Set("Origin", colabOrigin)
	req.Header.Set("Referer", colabOrigin+"/")
	req.Header.Set("X-Goog-Api-Key", colabAPIKey)
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
		_ = d.Store.Set(ctx, "colab_auth_state", "expired")
		_ = d.Store.Set(ctx, "colab_cookie_fp", shortFingerprint(cookie))
		return nil, fmt.Errorf("%w: HTTP %d: %s", ErrExpiredAuth, resp.StatusCode, snippet(body))
	case http.StatusTooManyRequests:
		return nil, fmt.Errorf("%w: HTTP 429: %s", ErrRateLimited, snippet(body))
	default:
		return nil, fmt.Errorf("colab: HTTP %d: %s", resp.StatusCode, snippet(body))
	}

	balance, accelerators, err := parseColabUserInfo(body)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	out := []model.Snapshot{
		{
			Provider:  "colab",
			Metric:    "compute_units",
			Label:     "Compute units",
			Remaining: balance,
			Unit:      "units",
			Status:    model.StatusOK,
			UpdatedAt: now,
		},
	}
	if balance <= 0 {
		out[0].Status = model.StatusWarning
	}
	if len(accelerators) > 0 {
		out = append(out, model.Snapshot{
			Provider:  "colab",
			Metric:    "available_accelerators",
			Label:     "Accelerators available",
			Remaining: float64(len(accelerators)),
			Kind:      "note",
			Note:      strings.Join(accelerators, " · "),
			Status:    model.StatusOK,
			UpdatedAt: now,
		})
	}
	d.tagSession(ctx, "colab", cookie, out)
	return out, nil
}

// parseColabUserInfo reads the positional gRPC reply.
func parseColabUserInfo(body []byte) (float64, []string, error) {
	var fields collectResponse
	if err := json.Unmarshal(body, &fields); err != nil {
		return 0, nil, fmt.Errorf("colab: decode: %w", err)
	}
	if len(fields) < 2 {
		return 0, nil, fmt.Errorf("colab: unexpected reply with %d fields", len(fields))
	}

	var balance float64
	if err := json.Unmarshal(fields[1], &balance); err != nil {
		return 0, nil, fmt.Errorf("colab: balance field is not a number: %w", err)
	}

	var accelerators []string
	if len(fields) > 2 {
		var groups [][]json.RawMessage
		if json.Unmarshal(fields[2], &groups) == nil {
			for _, group := range groups {
				for _, item := range group {
					var list []string
					if json.Unmarshal(item, &list) == nil {
						accelerators = append(accelerators, list...)
					}
				}
			}
		}
	}
	return balance, accelerators, nil
}

// sapisidAuth builds Google's cookie-based Authorization header.
func sapisidAuth(cookieHeader, origin string) (string, error) {
	jar := map[string]string{}
	for _, part := range strings.Split(cookieHeader, ";") {
		name, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok {
			jar[name] = value
		}
	}

	sapisid := jar["SAPISID"]
	if sapisid == "" {
		return "", fmt.Errorf("SAPISID cookie missing")
	}
	firstParty := firstNonEmpty(jar["__Secure-1PAPISID"], sapisid)
	thirdParty := firstNonEmpty(jar["__Secure-3PAPISID"], sapisid)

	ts := time.Now().Unix()
	hash := func(value string) string {
		sum := sha1.Sum([]byte(fmt.Sprintf("%d %s %s", ts, value, origin)))
		return hex.EncodeToString(sum[:])
	}
	return fmt.Sprintf("SAPISIDHASH %d_%s SAPISID1PHASH %d_%s SAPISID3PHASH %d_%s",
		ts, hash(sapisid), ts, hash(firstParty), ts, hash(thirdParty)), nil
}

// colabCookie prefers the live browser session over a pasted cookie.
func colabCookie(ctx context.Context, d *Deps) string {
	if header, ok := d.BrowserCookies(ctx, "google.com", googleCookieNames); ok {
		return header
	}
	return d.Setting(ctx, "colab_cookie")
}

// googleCookieNames are the auth-carrying Google cookies.
var googleCookieNames = []string{
	"SID", "HSID", "SSID", "APISID", "SAPISID",
	"__Secure-1PSID", "__Secure-3PSID", "__Secure-1PAPISID", "__Secure-3PAPISID",
	"__Secure-1PSIDTS", "__Secure-3PSIDTS", "LSID", "OSID", "__Secure-OSID",
	"SIDCC", "__Secure-1PSIDCC", "__Secure-3PSIDCC",
}
