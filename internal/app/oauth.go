package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/abcdlsj/gauge/internal/engine"
	"github.com/abcdlsj/gauge/internal/model"
	"github.com/abcdlsj/gauge/internal/providers"
	"github.com/abcdlsj/gauge/internal/store"
)

// oauthBroker runs the loopback OAuth flow that binds an Antigravity account to
// the panel. The refresh token it lands in kv_store is minted for this app, so
// the panel never borrows credentials from another tool.
type oauthBroker struct {
	store  *store.Store
	engine *engine.Engine
	log    *log.Logger
	client *http.Client

	mu      sync.Mutex
	pending map[string]pendingAuth
}

type pendingAuth struct {
	verifier string
	redirect string
	slot     int
	expires  time.Time
}

const oauthStateTTL = 10 * time.Minute

func newOAuthBroker(st *store.Store, eng *engine.Engine, logger *log.Logger) *oauthBroker {
	return &oauthBroker{
		store:   st,
		engine:  eng,
		log:     logger,
		client:  &http.Client{Timeout: 20 * time.Second},
		pending: make(map[string]pendingAuth),
	}
}

// handleStart returns the Google consent URL for the browser to open.
func (b *oauthBroker) handleStart(w http.ResponseWriter, r *http.Request) {
	redirect := callbackURL(r)

	verifier, err := randomToken(32)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	state, err := randomToken(16)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])

	b.mu.Lock()
	for k, v := range b.pending {
		if time.Now().After(v.expires) {
			delete(b.pending, k)
		}
	}
	slot := 1
	if v, err := strconv.Atoi(r.URL.Query().Get("slot")); err == nil && v >= 1 && v <= 3 {
		slot = v
	}
	b.pending[state] = pendingAuth{verifier: verifier, redirect: redirect, slot: slot, expires: time.Now().Add(oauthStateTTL)}
	b.mu.Unlock()

	params := url.Values{}
	params.Set("client_id", oauthClientID(r.Context(), b.store))
	params.Set("redirect_uri", redirect)
	params.Set("response_type", "code")
	params.Set("scope", strings.Join(providers.AntigravityScopes, " "))
	params.Set("state", state)
	params.Set("code_challenge", challenge)
	params.Set("code_challenge_method", "S256")
	params.Set("access_type", "offline")
	params.Set("prompt", "consent")

	writeJSON(w, http.StatusOK, map[string]string{
		"url": "https://accounts.google.com/o/oauth2/v2/auth?" + params.Encode(),
	})
}

// handleCallback exchanges the authorization code and stores the refresh token.
func (b *oauthBroker) handleCallback(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	if errParam := query.Get("error"); errParam != "" {
		renderCallback(w, false, "Authorization failed: "+errParam)
		return
	}

	state := query.Get("state")
	code := query.Get("code")

	b.mu.Lock()
	p, ok := b.pending[state]
	delete(b.pending, state)
	b.mu.Unlock()

	if !ok || time.Now().After(p.expires) {
		renderCallback(w, false, "This sign-in link expired. Start again from the panel.")
		return
	}
	if code == "" {
		renderCallback(w, false, "Google did not return an authorization code.")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	refresh, access, err := b.exchange(ctx, code, p)
	if err != nil {
		b.log.Printf("oauth: exchange failed: %v", err)
		renderCallback(w, false, "Token exchange failed: "+err.Error())
		return
	}

	key := "antigravity_refresh_token"
	if p.slot > 1 {
		key = fmt.Sprintf("antigravity_refresh_token_%d", p.slot)
	}
	if err := b.store.Set(ctx, key, refresh); err != nil {
		renderCallback(w, false, "Could not store the credential: "+err.Error())
		return
	}
	if account, err := b.userInfo(ctx, access); err == nil {
		if p.slot > 1 {
			if raw, err := json.Marshal(account); err == nil {
				_ = b.store.Set(ctx, fmt.Sprintf("antigravity_account_%d", p.slot), string(raw))
			}
		}
	}
	b.engine.ResetCooldown()
	b.log.Println("oauth: Antigravity account connected")
	renderCallback(w, true, "Account connected. The panel refreshes within a few seconds.")
}

// exchange trades the authorization code for a refresh token.
func (b *oauthBroker) exchange(ctx context.Context, code string, p pendingAuth) (string, string, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", p.redirect)
	form.Set("code_verifier", p.verifier)

	clientID, clientSecret := oauthClient(context.Background(), b.store)
	form.Set("client_id", clientID)
	form.Set("client_secret", clientSecret)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://oauth2.googleapis.com/token",
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := b.client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()

	var out struct {
		AccessToken      string `json:"access_token"`
		RefreshToken     string `json:"refresh_token"`
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", "", fmt.Errorf("decode token response: %w", err)
	}
	if out.RefreshToken == "" {
		return "", "", fmt.Errorf("%s %s", out.Error, out.ErrorDescription)
	}
	return out.RefreshToken, out.AccessToken, nil
}

// userInfo resolves the identity behind the freshly minted token.
func (b *oauthBroker) userInfo(ctx context.Context, accessToken string) (model.Account, error) {
	var out model.Account
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://www.googleapis.com/oauth2/v3/userinfo", nil)
	if err != nil {
		return out, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := b.client.Do(req)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()

	var payload struct {
		Name    string `json:"name"`
		Email   string `json:"email"`
		Picture string `json:"picture"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return out, err
	}
	return model.Account{Name: payload.Name, Email: payload.Email, Picture: payload.Picture}, nil
}

// callbackURL mirrors the address the operator used to reach the panel, which
// keeps the loopback redirect consistent with what Google sees.
func callbackURL(r *http.Request) string {
	if v := r.URL.Query().Get("redirect"); v != "" {
		return v
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host + "/oauth/callback"
}

// oauthClient resolves the OAuth client used for the sign-in flow.
func oauthClient(ctx context.Context, st *store.Store) (string, string) {
	id := providers.AntigravityClientID()
	secret := providers.AntigravityClientSecret()
	if v, ok, _ := st.Get(ctx, "antigravity_client_id"); ok && v != "" {
		id = v
	}
	if v, ok, _ := st.Get(ctx, "antigravity_client_secret"); ok && v != "" {
		secret = v
	}
	return id, secret
}

func oauthClientID(ctx context.Context, st *store.Store) string {
	id, _ := oauthClient(ctx, st)
	return id
}

// randomToken returns a base64url string of n random bytes.
func randomToken(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// renderCallback shows a minimal result page and bounces back to the panel.
func renderCallback(w http.ResponseWriter, ok bool, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	title := "Connected"
	color := "#12805c"
	if !ok {
		title = "Sign-in failed"
		color = "#c8403a"
	}
	fmt.Fprintf(w, `<!DOCTYPE html><html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>GPanel · %s</title>
<meta http-equiv="refresh" content="2;url=/#settings">
<style>
body{margin:0;min-height:100vh;display:grid;place-items:center;background:#f7f7f4;color:#17191d;
font:15px/1.5 "Avenir Next",Avenir,-apple-system,sans-serif}
.card{max-width:380px;padding:28px 30px;border-radius:14px;background:#fff;text-align:center}
h1{margin:0 0 8px;font-size:17px;color:%s}
p{margin:0;color:#71767e;font-size:13px}
@media (prefers-color-scheme:dark){body{background:#0b0c0e;color:#e9eaec}.card{background:#131519}
p{color:#8c919a}}
</style></head><body><div class="card"><h1>%s</h1><p>%s</p></div></body></html>`,
		title, color, title, message)
}
