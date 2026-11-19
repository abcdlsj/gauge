// Package providers implements the four quota collectors. Every collector is
// read-only: it talks to the upstream API, returns snapshots, and lets the
// engine persist them.
package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/abcdlsj/gauge/internal/browsercookie"
	"github.com/abcdlsj/gauge/internal/model"
	"github.com/abcdlsj/gauge/internal/store"
)

// Sentinel errors the engine maps onto provider status values.
var (
	ErrNotConfigured = errors.New("not configured")
	ErrExpiredAuth   = errors.New("credential rejected")
	ErrRateLimited   = errors.New("rate limited")
)

// Provider is one upstream quota source.
type Provider interface {
	Name() string
	Title() string
	Interval() time.Duration
	Collect(ctx context.Context, d *Deps) ([]model.Snapshot, error)
}

// Deps is the dependency bundle handed to every Collect call. It carries a
// shared HTTP client (routed by the proxy setting), the resolver behind it and
// a token cache.
type Deps struct {
	Store  *store.Store
	Client *http.Client
	Proxy  *Proxy
	Log    *log.Logger

	mu         sync.Mutex
	tokens     map[string]cachedToken
	browser    *browsercookie.Chrome
	accounts   []model.Account
	accountsAt time.Time
}

// minInterval is the floor each collector will accept from settings.
var minInterval = map[string]time.Duration{
	"antigravity":    5 * time.Minute,
	"colab":          30 * time.Minute,
	"google-flow":    30 * time.Minute,
	"gcp-monitoring": 10 * time.Minute,
	"gcp-billing":    60 * time.Minute,
	"commandcode":    2 * time.Minute,
	"opendesign":     2 * time.Minute,
}

// IntervalOverride resolves a user-configured cadence, clamped to the floor.
func (d *Deps) IntervalOverride(ctx context.Context, name string, def time.Duration) time.Duration {
	raw := d.Setting(ctx, "refresh_"+name+"_minutes")
	if raw == "" {
		return def
	}
	minutes, err := strconv.Atoi(raw)
	if err != nil || minutes <= 0 {
		return def
	}
	out := time.Duration(minutes) * time.Minute
	if floor, ok := minInterval[name]; ok && out < floor {
		out = floor
	}
	return out
}

// AccountSource is implemented by providers that know about signed-in accounts.
type AccountSource interface {
	Accounts(ctx context.Context) []model.Account
}

// SetAccounts refreshes the cached identity list and persists it, so the
// window can paint avatars before the first collection finishes.
func (d *Deps) SetAccounts(ctx context.Context, list []model.Account) {
	d.mu.Lock()
	d.accounts = list
	d.accountsAt = time.Now()
	d.mu.Unlock()

	if raw, err := json.Marshal(list); err == nil {
		_ = d.Store.Set(ctx, "accounts_cache", string(raw))
	}
}

// LoadCachedAccounts restores the identity list written by the last run.
func (d *Deps) LoadCachedAccounts(ctx context.Context) {
	raw, ok, err := d.Store.Get(ctx, "accounts_cache")
	if err != nil || !ok || raw == "" {
		return
	}
	var list []model.Account
	if err := json.Unmarshal([]byte(raw), &list); err != nil || len(list) == 0 {
		return
	}
	d.mu.Lock()
	d.accounts = list
	d.accountsAt = time.Now().Add(-2 * time.Hour) // force a refresh soon
	d.mu.Unlock()
}

// Accounts returns the cached identities, refreshing them when stale.
func (d *Deps) Accounts() []model.Account {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]model.Account, len(d.accounts))
	copy(out, d.accounts)
	return out
}

// accountLabel reads the stored identity for an extra account slot.
func (d *Deps) accountLabel(ctx context.Context, slot int) model.Account {
	raw, ok, err := d.Store.Get(ctx, fmt.Sprintf("antigravity_account_%d", slot))
	if err != nil || !ok || raw == "" {
		return model.Account{Email: fmt.Sprintf("account %d", slot)}
	}
	var account model.Account
	if err := json.Unmarshal([]byte(raw), &account); err != nil {
		return model.Account{Email: fmt.Sprintf("account %d", slot)}
	}
	return account
}

// AccountsStale reports whether the cache needs a refresh.
func (d *Deps) AccountsStale() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return time.Since(d.accountsAt) > time.Minute
}

type cachedToken struct {
	value   string
	expires time.Time
}

// NewDeps builds the shared collector dependencies.
func NewDeps(st *store.Store, logger *log.Logger) *Deps {
	proxy := NewProxy(st)
	d := &Deps{
		Store:  st,
		Proxy:  proxy,
		Client: proxy.Client(30 * time.Second),
		Log:    logger,
		tokens: make(map[string]cachedToken),
	}
	// Reading the browser store is optional: without it the collectors fall
	// back to whatever cookie was pasted into the settings page.
	if chrome, err := browsercookie.NewChrome(); err == nil {
		d.browser = chrome
	}
	return d
}

// BrowserCookies returns a Cookie header for the domain, using the local Chrome
// profile when the operator enabled it. Nothing is persisted.
func (d *Deps) BrowserCookies(ctx context.Context, domain string, names []string) (string, bool) {
	if d.browser == nil {
		return "", false
	}
	if d.SettingDefault(ctx, "browser_cookies", "chrome") != "chrome" {
		return "", false
	}
	header, err := d.browser.Header(domain, names)
	if err != nil {
		d.Log.Printf("browser cookies: %v", err)
		return "", false
	}
	return header, true
}

// Setting returns a stored setting, or "" when unset. Read errors are logged
// and treated as missing so a corrupted row never wedges a collector.
func (d *Deps) Setting(ctx context.Context, key string) string {
	v, _, err := d.Store.Get(ctx, key)
	if err != nil {
		d.Log.Printf("settings: %s: %v", key, err)
		return ""
	}
	return v
}

// SettingDefault returns the stored setting or a fallback.
func (d *Deps) SettingDefault(ctx context.Context, key, def string) string {
	if v := d.Setting(ctx, key); v != "" {
		return v
	}
	return def
}

// SettingFloat parses a numeric setting, falling back to def.
func (d *Deps) SettingFloat(ctx context.Context, key string, def float64) float64 {
	v := d.Setting(ctx, key)
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return def
	}
	return f
}

// tokenFor returns a cached token still valid for at least a minute.
func (d *Deps) tokenFor(key string) (string, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	t, ok := d.tokens[key]
	if !ok || time.Now().After(t.expires.Add(-time.Minute)) {
		return "", false
	}
	return t.value, true
}

// cacheToken stores a token with its expiry.
func (d *Deps) cacheToken(key, value string, expires time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.tokens[key] = cachedToken{value: value, expires: expires}
}

// FlushTokens drops every cached access token. Collectors call it after a 401
// so the next cycle mints a fresh one instead of retrying a dead token.
func (d *Deps) FlushTokens() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.tokens = make(map[string]cachedToken)
}

// cacheKey builds a stable cache key from the scope list and a credential
// fingerprint, so switching accounts never reuses a stale token.
func cacheKey(scopes []string, fingerprint string) string {
	key := ""
	for _, s := range scopes {
		key += s + ","
	}
	return key + "|" + fingerprint
}

// shortFingerprint hashes credentials down to something non-reversible, which
// is enough to key the token cache and the Colab pause marker.
func shortFingerprint(parts ...string) string {
	h := fnv.New64a()
	for _, p := range parts {
		_, _ = h.Write([]byte(p))
	}
	return fmt.Sprintf("%016x", h.Sum64())
}

// readLimited reads at most n bytes from r.
func readLimited(r io.Reader, n int64) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r, n))
}
