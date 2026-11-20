package providers

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// tokenEndpoint trades a refresh token for an access token. A variable so
// tests can point it at a local server.
var tokenEndpoint = "https://oauth2.googleapis.com/token"

// AuthSource selects which credential set a collector should use. Antigravity
// and Cloud APIs usually belong to different accounts, so the panel keeps two
// independent token chains.
type AuthSource string

const (
	SourceAntigravity AuthSource = "antigravity"
	// SourceAntigravityAlt plus a slot suffix ("antigravity-2") selects an extra
	// account connected through the OAuth flow.
	SourceAntigravityAlt AuthSource = "antigravity-"
	SourceGCP            AuthSource = "gcp"
)

// Auth is a usable access token plus any headers the API expects alongside it.
type Auth struct {
	Token  string
	Header http.Header
}

// credMaterial is the resolved credential set for one auth source.
type credMaterial struct {
	refreshToken string
	clientID     string
	clientSecret string
	saJSON       string
	quotaProject string
	fingerprint  string
}

// GoogleAuth returns a cached or freshly minted access token for the source.
func (d *Deps) GoogleAuth(ctx context.Context, scopes []string, src AuthSource) (*Auth, error) {
	c, err := d.resolveCreds(ctx, src)
	if err != nil {
		return nil, err
	}
	key := cacheKey(scopes, string(src)+"|"+c.fingerprint)
	if tok, ok := d.tokenFor(key); ok {
		return &Auth{Token: tok, Header: quotaHeader(c)}, nil
	}

	var (
		tok string
		exp time.Time
	)
	if c.saJSON != "" {
		tok, exp, err = serviceAccountToken(ctx, d.Client, c.saJSON, scopes)
	} else {
		tok, exp, err = refreshTokenGrant(ctx, d.Client, c, scopes)
	}
	if err != nil {
		return nil, err
	}
	d.cacheToken(key, tok, exp)
	return &Auth{Token: tok, Header: quotaHeader(c)}, nil
}

// quotaHeader attaches the billing/quota project that user credentials need.
func quotaHeader(c credMaterial) http.Header {
	h := http.Header{}
	if c.saJSON == "" && c.quotaProject != "" {
		h.Set("x-goog-user-project", c.quotaProject)
	}
	return h
}

// resolveCreds walks the settings; the GCP chain then falls back to the gcloud
// ADC file.
func (d *Deps) resolveCreds(ctx context.Context, src AuthSource) (credMaterial, error) {
	var c credMaterial

	// A service account only ever authenticates the Cloud APIs. The Antigravity
	// quota endpoint rejects any token not minted for its own OAuth client, so
	// letting gcp_sa_json reach that chain broke every Antigravity account.
	if src == SourceGCP {
		c.saJSON = d.Setting(ctx, "gcp_sa_json")
		if c.saJSON == "" {
			c.saJSON = d.adcServiceAccount(ctx)
		}
	}

	switch {
	case src == SourceAntigravity || strings.HasPrefix(string(src), string(SourceAntigravityAlt)):
		// Only tokens Gauge was given, through its own sign-in or pasted into
		// settings. The clients' credential (Keychain, agy's token file) is
		// theirs; Gauge reads the running IDE instead. Nothing here falls
		// back to the GCP chain: its google_* token and client belong to a
		// different OAuth client, which the quota endpoint rejects, and the
		// client must be the one the sign-in used (oauthClient in app).
		key := "antigravity_refresh_token"
		if src != SourceAntigravity {
			// The caller passes the slot in the source name ("antigravity-2").
			key += "_" + strings.TrimPrefix(string(src), string(SourceAntigravityAlt))
		}
		c.refreshToken = d.Setting(ctx, key)
		c.clientID = d.SettingDefault(ctx, "antigravity_client_id", antigravityClientID)
		c.clientSecret = d.SettingDefault(ctx, "antigravity_client_secret", antigravityClientSecret)
	case src == SourceGCP:
		adc := d.adcUserCreds(ctx)
		c.refreshToken = firstNonEmpty(
			d.Setting(ctx, "gcp_refresh_token"),
			d.Setting(ctx, "google_refresh_token"),
			adc.refreshToken,
		)
		c.clientID = firstNonEmpty(d.Setting(ctx, "gcp_client_id"), d.Setting(ctx, "google_client_id"), adc.clientID)
		c.clientSecret = firstNonEmpty(d.Setting(ctx, "gcp_client_secret"), d.Setting(ctx, "google_client_secret"), adc.clientSecret)
		c.quotaProject = firstNonEmpty(
			d.Setting(ctx, "gcp_quota_project"),
			adc.quotaProject,
			d.Setting(ctx, "gcp_project_id"),
		)
	default:
		return c, fmt.Errorf("%w: unknown auth source %q", ErrNotConfigured, src)
	}

	if c.saJSON == "" {
		if c.refreshToken == "" {
			return c, fmt.Errorf("%w: no credentials for %s", ErrNotConfigured, src)
		}
		if c.clientID == "" || c.clientSecret == "" {
			return c, fmt.Errorf("%w: client id/secret missing for %s", ErrNotConfigured, src)
		}
	}
	c.fingerprint = shortFingerprint(c.refreshToken, c.clientID, c.saJSON, c.quotaProject)
	return c, nil
}

// refreshTokenGrant exchanges a long-lived refresh token for an access token.
func refreshTokenGrant(ctx context.Context, client *http.Client, c credMaterial, _ []string) (string, time.Time, error) {
	form := map[string]string{
		"grant_type":    "refresh_token",
		"refresh_token": c.refreshToken,
		"client_id":     c.clientID,
		"client_secret": c.clientSecret,
	}
	body, _ := json.Marshal(form)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenEndpoint, bytes.NewReader(body))
	if err != nil {
		return "", time.Time{}, err
	}
	req.Header.Set("Content-Type", "application/json")

	var out struct {
		AccessToken      string `json:"access_token"`
		ExpiresIn        int    `json:"expires_in"`
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	if err := doJSON(client, req, &out); err != nil {
		return "", time.Time{}, err
	}
	if out.AccessToken == "" {
		return "", time.Time{}, fmt.Errorf("%w: refresh_token grant: %s %s", ErrExpiredAuth, out.Error, out.ErrorDescription)
	}
	ttl := out.ExpiresIn
	if ttl <= 0 {
		ttl = 3600
	}
	return out.AccessToken, time.Now().Add(time.Duration(ttl) * time.Second), nil
}

// serviceAccountToken signs a JWT assertion and trades it for an access token.
func serviceAccountToken(ctx context.Context, client *http.Client, rawJSON string, scopes []string) (string, time.Time, error) {
	var sa struct {
		ClientEmail string `json:"client_email"`
		PrivateKey  string `json:"private_key"`
		TokenURI    string `json:"token_uri"`
	}
	if err := json.Unmarshal([]byte(rawJSON), &sa); err != nil {
		return "", time.Time{}, fmt.Errorf("%w: service account json: %v", ErrNotConfigured, err)
	}
	if sa.ClientEmail == "" || sa.PrivateKey == "" {
		return "", time.Time{}, fmt.Errorf("%w: service account json is missing fields", ErrNotConfigured)
	}
	if sa.TokenURI == "" {
		sa.TokenURI = tokenEndpoint
	}

	key, err := parseRSAPrivateKey(sa.PrivateKey)
	if err != nil {
		return "", time.Time{}, err
	}

	now := time.Now()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims := map[string]any{
		"iss":   sa.ClientEmail,
		"scope": strings.Join(scopes, " "),
		"aud":   sa.TokenURI,
		"iat":   now.Unix(),
		"exp":   now.Add(time.Hour).Unix(),
	}
	claimJSON, _ := json.Marshal(claims)
	payload := base64.RawURLEncoding.EncodeToString(claimJSON)
	signingInput := header + "." + payload

	digest := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(nil, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", time.Time{}, fmt.Errorf("sign jwt: %w", err)
	}
	assertion := signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)

	form := "grant_type=urn%3Aietf%3Aparams%3Aoauth%3Agrant-type%3Ajwt-bearer&assertion=" + assertion
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sa.TokenURI, strings.NewReader(form))
	if err != nil {
		return "", time.Time{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := doJSON(client, req, &out); err != nil {
		return "", time.Time{}, err
	}
	if out.AccessToken == "" {
		return "", time.Time{}, fmt.Errorf("%w: service account token exchange failed", ErrExpiredAuth)
	}
	ttl := out.ExpiresIn
	if ttl <= 0 {
		ttl = 3600
	}
	return out.AccessToken, now.Add(time.Duration(ttl) * time.Second), nil
}

func parseRSAPrivateKey(pemKey string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemKey))
	if block == nil {
		return nil, fmt.Errorf("%w: invalid PEM in service account json", ErrNotConfigured)
	}
	if k, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		if rk, ok := k.(*rsa.PrivateKey); ok {
			return rk, nil
		}
		return nil, fmt.Errorf("%w: service account key is not RSA", ErrNotConfigured)
	}
	k, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse service account key: %w", err)
	}
	return k, nil
}

// --- local credential discovery ---------------------------------------------

// adcUserCreds reads the gcloud Application Default Credentials file when it
// holds a user refresh token.
func (d *Deps) adcUserCreds(ctx context.Context) credMaterial {
	var c credMaterial
	raw := readJSONFile(adcPath(ctx, d))
	if raw == nil {
		return c
	}
	if t, _ := raw["type"].(string); t != "authorized_user" {
		return c
	}
	c.refreshToken = stringField(raw, "refresh_token")
	c.clientID = stringField(raw, "client_id")
	c.clientSecret = stringField(raw, "client_secret")
	c.quotaProject = stringField(raw, "quota_project_id")
	return c
}

// adcServiceAccount returns a service-account JSON body from the ADC file.
func (d *Deps) adcServiceAccount(ctx context.Context) string {
	path := adcPath(ctx, d)
	raw := readJSONFile(path)
	if raw == nil {
		return ""
	}
	if t, _ := raw["type"].(string); t != "service_account" {
		return ""
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}

// adcPath resolves the ADC file: explicit setting, then env, then default.
func adcPath(ctx context.Context, d *Deps) string {
	if p := expandHome(d.Setting(ctx, "gcp_adc_path")); p != "" {
		return p
	}
	if p := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS"); p != "" {
		return expandHome(p)
	}
	return expandHome("~/.config/gcloud/application_default_credentials.json")
}

func readJSONFile(path string) map[string]any {
	if path == "" {
		return nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil
	}
	return out
}

func stringField(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

// --- shared helpers ---------------------------------------------------------

// doJSON executes a request and decodes the body, mapping 401/403 to
// ErrExpiredAuth so the engine can flag the provider.
func doJSON(client *http.Client, req *http.Request, out any) error {
	req.Header.Set("Accept", "application/json")
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", "gauge/1.0")
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("%w: HTTP %d: %s", ErrExpiredAuth, resp.StatusCode, snippet(body))
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return fmt.Errorf("%w: HTTP 429: %s", ErrRateLimited, snippet(body))
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, snippet(body))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
