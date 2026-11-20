package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
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
	// results holds the outcome of each finished sign-in, by state, so the
	// settings page can say what happened instead of guessing from the list.
	results map[string]oauthResult
}

// oauthResult is what /api/oauth/status reports for one sign-in.
type oauthResult struct {
	Done    bool   `json:"done"`
	OK      bool   `json:"ok"`
	Slot    int    `json:"slot,omitempty"`
	Email   string `json:"email,omitempty"`
	Message string `json:"message,omitempty"`
	at      time.Time
}

type pendingAuth struct {
	verifier string
	redirect string
	slot     int
	expires  time.Time
}

const oauthStateTTL = 10 * time.Minute

// newOAuthBroker wires the consent flow. The client follows the proxy setting,
// because the token endpoint is as unreachable as everything else in a network
// that needs one.
func newOAuthBroker(st *store.Store, eng *engine.Engine, proxy *providers.Proxy, logger *log.Logger) *oauthBroker {
	return &oauthBroker{
		store:   st,
		engine:  eng,
		log:     logger,
		client:  proxy.Client(20 * time.Second),
		pending: make(map[string]pendingAuth),
		results: make(map[string]oauthResult),
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
		"url":   "https://accounts.google.com/o/oauth2/v2/auth?" + params.Encode(),
		"state": state,
	})
}

// handleStatus reports how a sign-in started with handleStart ended.
func (b *oauthBroker) handleStatus(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	b.mu.Lock()
	res, ok := b.results[state]
	b.mu.Unlock()
	if !ok {
		writeJSON(w, http.StatusOK, oauthResult{})
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// finish records a sign-in's outcome and shows it in the browser tab.
func (b *oauthBroker) finish(w http.ResponseWriter, state string, res oauthResult) {
	res.Done, res.at = true, time.Now()
	b.mu.Lock()
	for k, v := range b.results {
		if time.Since(v.at) > oauthStateTTL {
			delete(b.results, k)
		}
	}
	if state != "" {
		b.results[state] = res
	}
	b.mu.Unlock()
	renderCallback(w, res.OK, res.Message)
}

// handleCallback exchanges the authorization code and stores the refresh token.
func (b *oauthBroker) handleCallback(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	state := query.Get("state")

	b.mu.Lock()
	p, ok := b.pending[state]
	delete(b.pending, state)
	b.mu.Unlock()

	if errParam := query.Get("error"); errParam != "" {
		msg := "Authorization failed: " + errParam
		if errParam == "access_denied" {
			msg = "Sign-in was cancelled. You can close this tab and try again from Gauge."
		}
		b.finish(w, state, oauthResult{Message: msg})
		return
	}

	code := query.Get("code")

	if !ok || time.Now().After(p.expires) {
		b.finish(w, state, oauthResult{Message: "This sign-in link expired. Start again from Gauge."})
		return
	}
	if code == "" {
		b.finish(w, state, oauthResult{Message: "Google did not return an authorization code."})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	refresh, access, err := b.exchange(ctx, code, p)
	if err != nil {
		b.log.Printf("oauth: exchange failed: %v", err)
		b.finish(w, state, oauthResult{Message: "Token exchange failed: " + err.Error()})
		return
	}

	account, infoErr := b.userInfo(ctx, access)
	if infoErr != nil {
		b.log.Printf("oauth: slot %d: cannot read who signed in: %v", p.slot, infoErr)
	}
	slot, note := placeSignIn(ctx, b.store, p.slot, account.Email)

	key := "antigravity_refresh_token"
	if slot > 1 {
		key = fmt.Sprintf("antigravity_refresh_token_%d", slot)
	}
	if err := b.store.Set(ctx, key, refresh); err != nil {
		b.finish(w, state, oauthResult{Message: "Could not store the credential: " + err.Error()})
		return
	}
	// Name the account right away so settings shows it before the first
	// collection. The collector re-reads it against the token fingerprint.
	if infoErr == nil {
		if raw, err := json.Marshal(account); err == nil {
			_ = b.store.Set(ctx, fmt.Sprintf("antigravity_account_%d", slot), string(raw))
		}
	} else {
		_ = b.store.Delete(ctx, fmt.Sprintf("antigravity_account_%d", slot))
	}
	b.engine.ResetCooldown("antigravity")
	// Collect right away: the next scheduled pass is minutes away, and until
	// it runs the account cache still lists only the old identities.
	go b.engine.RunOnce(context.Background(), "antigravity", true)
	b.log.Printf("oauth: Antigravity account connected to slot %d", slot)
	b.finish(w, state, oauthResult{OK: true, Slot: slot, Email: account.Email,
		Message: note + " You can close this tab and return to Gauge in the menu bar."})
}

// signInEmail is the account stored for a slot's sign-in.
func signInEmail(ctx context.Context, st *store.Store, slot int) string {
	raw, ok, err := st.Get(ctx, fmt.Sprintf("antigravity_account_%d", slot))
	if err != nil || !ok {
		return ""
	}
	var a model.Account
	_ = json.Unmarshal([]byte(raw), &a)
	return a.Email
}

// placeSignIn picks the slot a new sign-in lands in. An account Gauge already
// shows keeps its slot, so signing in again refreshes it instead of adding a
// copy. The account the IDE is on may take slot 1 as that account's fallback;
// any other sign-in gets a slot of its own, because it has to outlive whichever
// account the IDE happens to use.
func placeSignIn(ctx context.Context, st *store.Store, asked int, email string) (int, string) {
	if slot := providers.AntigravitySlotFor(ctx, st, email); slot > 0 {
		switch {
		case slot == 1 && asked != 1:
			note := fmt.Sprintf("%s is the account the Antigravity IDE uses, so it was saved as that account's sign-in for when the IDE is closed.", email)
			if prev := signInEmail(ctx, st, 1); prev != "" && !strings.EqualFold(prev, email) {
				note += fmt.Sprintf(" It replaces the sign-in for %s.", prev)
			}
			return 1, note
		case slot != asked:
			return slot, fmt.Sprintf("%s was already connected as account %d; its sign-in was renewed there.", email, slot)
		}
		return slot, fmt.Sprintf("%s was signed in again.", email)
	}
	if asked == 1 {
		if ide := providers.AntigravityIDEEmail(ctx, st); ide != "" && email != "" && !strings.EqualFold(ide, email) {
			// Asking for slot 1 while the IDE is on someone else would tie this
			// sign-in to the IDE's next choice, so give it a slot of its own.
			if free := freeSignInSlot(ctx, st); free > 0 {
				return free, fmt.Sprintf("Connected %s as account %d. The IDE is on %s, so the sign-in stays with %s.", email, free, ide, email)
			}
			return 1, fmt.Sprintf("Connected %s. The IDE uses %s, so this sign-in is only read while the IDE is closed.", email, ide)
		}
	}
	if email == "" {
		return asked, "Account connected."
	}
	return asked, fmt.Sprintf("Connected %s.", email)
}

// freeSignInSlot is the first extra slot Gauge holds no sign-in for.
func freeSignInSlot(ctx context.Context, st *store.Store) int {
	for slot := 2; slot <= providers.MaxInstances(); slot++ {
		v, ok, err := st.Get(ctx, fmt.Sprintf("antigravity_refresh_token_%d", slot))
		if err == nil && (!ok || v == "") {
			return slot
		}
	}
	return 0
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
	return scheme + "://" + r.Host + "/oauth-callback"
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

// renderCallback shows a minimal result page. It never navigates anywhere:
// there is no page behind the popover to return to.
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
<title>Gauge · %s</title>
<style>
body{margin:0;min-height:100vh;display:grid;place-items:center;background:#f7f7f4;color:#17191d;
font:15px/1.5 "Avenir Next",Avenir,-apple-system,sans-serif}
.card{max-width:380px;padding:28px 30px;border-radius:14px;background:#fff;text-align:center}
h1{margin:0 0 8px;font-size:17px;color:%s}
p{margin:0;color:#71767e;font-size:13px}
@media (prefers-color-scheme:dark){body{background:#0b0c0e;color:#e9eaec}.card{background:#131519}
p{color:#8c919a}}
</style></head><body><div class="card"><h1>%s</h1><p>%s</p></div></body></html>`,
		title, color, title, html.EscapeString(message))
}
