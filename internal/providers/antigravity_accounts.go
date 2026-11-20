package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/abcdlsj/gauge/internal/model"
	"github.com/abcdlsj/gauge/internal/store"
)

// Antigravity identities.
//
// Slot 1 is the account the IDE is signed in to. While the IDE runs, its
// language server names that account; the reading is kept under
// ideAccountKey so the account keeps its name and avatar when the IDE closes.
//
// Every slot may also hold a token from Gauge's own sign-in (slot 1:
// antigravity_refresh_token; slot N: antigravity_refresh_token_N). The account
// behind that token is kept under antigravity_account_N, tagged with the
// token's fingerprint, so a replaced token never inherits the old name.

// ideAccountKey holds the last account the running IDE reported.
const ideAccountKey = "antigravity_ide_account"

// storedIdentity is an account plus the fingerprint of the token it was read
// with. Embedding keeps the JSON compatible with records written before the
// fingerprint existed.
type storedIdentity struct {
	model.Account
	FP string `json:"fp,omitempty"`
}

func identityKey(slot int) string { return fmt.Sprintf("antigravity_account_%d", slot) }

// refreshTokenKey is the setting that holds a slot's refresh token. Slot 1 keeps
// the historical key without a suffix.
func refreshTokenKey(slot int) string {
	if slot <= 1 {
		return "antigravity_refresh_token"
	}
	return fmt.Sprintf("antigravity_refresh_token_%d", slot)
}

// promoteSignIn moves slot 1's Gauge sign-in to a free account when the IDE has
// moved to a different one. An account is a single identity with two possible
// channels, so its sign-in belongs with the account rather than with the IDE's
// current choice; left on slot 1 it would stop being read the moment the IDE
// signed in anywhere else.
func (d *Deps) promoteSignIn(ctx context.Context, ideEmail string) {
	refresh := d.Setting(ctx, refreshTokenKey(1))
	if refresh == "" || ideEmail == "" {
		return
	}
	who, _ := d.signInIdentity(ctx, 1)
	if who.Email == "" || strings.EqualFold(who.Email, ideEmail) {
		return
	}
	target := 0
	for slot := 2; slot <= maxAccounts; slot++ {
		if d.Setting(ctx, refreshTokenKey(slot)) == "" {
			target = slot
			break
		}
	}
	if target == 0 {
		d.Log.Printf("antigravity: %s is signed in for account 1 and the IDE is on %s; no free account to move it to", who.Email, ideEmail)
		return
	}
	if err := d.Store.Set(ctx, refreshTokenKey(target), refresh); err != nil {
		d.Log.Printf("antigravity: moving %s's sign-in: %v", who.Email, err)
		return
	}
	if id := d.loadIdentity(ctx, identityKey(1)); id.Email != "" {
		d.saveIdentity(ctx, identityKey(target), id)
	}
	_ = d.Store.Delete(ctx, refreshTokenKey(1))
	_ = d.Store.Delete(ctx, identityKey(1))
	d.FlushTokens()
	d.Log.Printf("antigravity: %s's sign-in moved from account 1 to account %d; the IDE now uses %s", who.Email, target, ideEmail)
}

// shownElsewhere reports whether another account already displays this email.
func shownElsewhere(accounts []model.Account, email string) bool {
	if email == "" {
		return false
	}
	for _, a := range accounts {
		if a.Email != "" && strings.EqualFold(a.Email, email) {
			return true
		}
	}
	return false
}

// sourceFor is the auth source of a slot's Gauge sign-in.
func sourceFor(slot int) AuthSource {
	if slot <= 1 {
		return SourceAntigravity
	}
	return AuthSource(fmt.Sprintf("%s%d", SourceAntigravityAlt, slot))
}

func (d *Deps) loadIdentity(ctx context.Context, key string) storedIdentity {
	var out storedIdentity
	raw, ok, err := d.Store.Get(ctx, key)
	if err != nil || !ok || raw == "" {
		return out
	}
	_ = json.Unmarshal([]byte(raw), &out)
	return out
}

func (d *Deps) saveIdentity(ctx context.Context, key string, id storedIdentity) {
	if raw, err := json.Marshal(id); err == nil {
		_ = d.Store.Set(ctx, key, string(raw))
	}
}

// hasSignIn reports whether Gauge holds a token for the slot.
func (d *Deps) hasSignIn(ctx context.Context, slot int) bool {
	_, err := d.resolveCreds(ctx, sourceFor(slot))
	return err == nil
}

// signInIdentity is the account behind a slot's Gauge sign-in. A stored record
// is reused while it matches the current token; otherwise Google's userinfo
// endpoint is asked once and the answer stored, avatar included. The zero
// Account means no token, or no answer for a token Gauge has never seen.
// expired reports whether Google rejected the token.
func (d *Deps) signInIdentity(ctx context.Context, slot int) (account model.Account, expired bool) {
	src := sourceFor(slot)
	key := identityKey(slot)
	stored := d.loadIdentity(ctx, key)

	c, err := d.resolveCreds(ctx, src)
	if err != nil {
		return model.Account{}, false
	}
	if stored.FP == c.fingerprint && stored.Email != "" {
		return stored.Account, false
	}

	auth, err := d.GoogleAuth(ctx, AntigravityScopes, src)
	if err == nil {
		account, err = d.userInfo(ctx, auth)
		if err == nil {
			account.Picture = d.avatarData(ctx, account.Picture)
			d.saveIdentity(ctx, key, storedIdentity{Account: account, FP: c.fingerprint})
			return account, false
		}
	}
	d.Log.Printf("antigravity: account %d: cannot read who signed in: %v", slot, err)
	expired = errors.Is(err, ErrExpiredAuth)
	// A record without a fingerprint was written by the sign-in callback for
	// this very token; anything else may name a different account.
	if stored.FP == "" {
		return stored.Account, expired
	}
	return model.Account{}, expired
}

// userInfo asks Google who a token belongs to.
func (d *Deps) userInfo(ctx context.Context, auth *Auth) (model.Account, error) {
	var out model.Account
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, userInfoEndpoint, nil)
	if err != nil {
		return out, err
	}
	req.Header.Set("Authorization", "Bearer "+auth.Token)
	var payload struct {
		Name    string `json:"name"`
		Email   string `json:"email"`
		Picture string `json:"picture"`
	}
	if err := doJSON(d.Client, req, &payload); err != nil {
		return out, err
	}
	if payload.Email == "" {
		return out, fmt.Errorf("userinfo returned no email")
	}
	return model.Account{Name: payload.Name, Email: payload.Email, Picture: payload.Picture}, nil
}

// userInfoEndpoint is a variable so tests can point it at a local server.
var userInfoEndpoint = "https://www.googleapis.com/oauth2/v3/userinfo"

// rememberIDEAccount stores what the running IDE reported, fetching the
// avatar only when it changed.
func (d *Deps) rememberIDEAccount(ctx context.Context, account model.Account) {
	prev := d.loadIdentity(ctx, ideAccountKey)
	source := account.Picture
	switch {
	case source == "":
	case prev.FP == source && prev.Picture != "":
		// FP holds the picture URL here: same URL, keep the cached bytes.
		account.Picture = prev.Picture
	default:
		account.Picture = d.avatarData(ctx, source)
	}
	d.saveIdentity(ctx, ideAccountKey, storedIdentity{Account: account, FP: source})
}

// lastIDEAccount is the account the IDE reported the last time it ran.
func (d *Deps) lastIDEAccount(ctx context.Context) model.Account {
	a := d.loadIdentity(ctx, ideAccountKey).Account
	a.Slot, a.Primary = 1, true
	return a
}

// AntigravitySlotFor names the slot that already shows email: 1 for the IDE's
// account or slot 1's sign-in, N for an extra sign-in. 0 means none.
func AntigravitySlotFor(ctx context.Context, st *store.Store, email string) int {
	if email == "" {
		return 0
	}
	read := func(key string) string {
		var id storedIdentity
		if raw, ok, err := st.Get(ctx, key); err == nil && ok {
			_ = json.Unmarshal([]byte(raw), &id)
		}
		return id.Email
	}
	if strings.EqualFold(read(ideAccountKey), email) {
		return 1
	}
	for slot := 1; slot <= maxAccounts; slot++ {
		key := "antigravity_refresh_token"
		if slot > 1 {
			key = fmt.Sprintf("antigravity_refresh_token_%d", slot)
		}
		if v, ok, _ := st.Get(ctx, key); !ok || v == "" {
			continue
		}
		if strings.EqualFold(read(identityKey(slot)), email) {
			return slot
		}
	}
	return 0
}

// AntigravityIDEEmail is the account the IDE last reported.
func AntigravityIDEEmail(ctx context.Context, st *store.Store) string {
	var id storedIdentity
	if raw, ok, err := st.Get(ctx, ideAccountKey); err == nil && ok {
		_ = json.Unmarshal([]byte(raw), &id)
	}
	return id.Email
}
