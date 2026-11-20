package providers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/abcdlsj/gauge/internal/model"
)

// noIDE points hub discovery at an empty directory: the IDE is closed.
func noIDE(t *testing.T) {
	t.Helper()
	orig := hubRoots
	hubRoots = []string{t.TempDir()}
	t.Cleanup(func() { hubRoots = orig })
}

// fakeGoogle serves the token, userinfo and Cloud Code endpoints. emails maps a
// refresh token to the account it belongs to.
func fakeGoogle(t *testing.T, emails map[string]string) *atomic.Int32 {
	t.Helper()
	var quotaCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token":
			var form map[string]string
			_ = json.NewDecoder(r.Body).Decode(&form)
			_, _ = w.Write([]byte(`{"access_token":"at-` + form["refresh_token"] + `","expires_in":3600}`))
		case r.URL.Path == "/userinfo":
			token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer at-")
			_, _ = w.Write([]byte(`{"email":"` + emails[token] + `","name":"N"}`))
		case strings.HasSuffix(r.URL.Path, ":loadCodeAssist"):
			_, _ = w.Write([]byte(`{"paidTier":{"id":"g1-pro-tier"}}`))
		case strings.HasSuffix(r.URL.Path, ":fetchAvailableModels"):
			quotaCalls.Add(1)
			_, _ = w.Write([]byte(`{"models":{"gemini-3-pro":{"quotaInfo":{"remainingFraction":0.5}}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	origHosts, origUser, origToken := cloudCodeHosts, userInfoEndpoint, tokenEndpoint
	cloudCodeHosts = []string{srv.URL}
	userInfoEndpoint = srv.URL + "/userinfo"
	tokenEndpoint = srv.URL + "/token"
	t.Cleanup(func() { cloudCodeHosts, userInfoEndpoint, tokenEndpoint = origHosts, origUser, origToken })
	return &quotaCalls
}

// TestIDEClosedKeepsTheAccount covers the IDE closing with no Gauge sign-in:
// slot 1 stays listed under the identity the IDE last reported.
func TestIDEClosedKeepsTheAccount(t *testing.T) {
	ctx := context.Background()
	d := newTestDeps(t)
	noIDE(t)
	d.rememberIDEAccount(ctx, lastSeen("ide@x"))

	_, err := (Antigravity{}).Collect(ctx, d)
	if !errors.Is(err, ErrNotConfigured) || !strings.Contains(err.Error(), "not running") {
		t.Fatalf("err = %v, want a not-running hint", err)
	}
	accounts := d.Accounts()
	if len(accounts) != 1 || accounts[0].Email != "ide@x" || accounts[0].Slot != 1 {
		t.Fatalf("accounts = %+v, want the remembered IDE account", accounts)
	}
	if src := d.Setting(ctx, "antigravity_source"); src != sourceIDEOffline {
		t.Fatalf("source = %q", src)
	}
}

// TestIDEClosedNamesTheSignedInAccount checks slot 1 is labelled with the
// account its token belongs to, not the one the IDE used last.
func TestIDEClosedNamesTheSignedInAccount(t *testing.T) {
	ctx := context.Background()
	d := newTestDeps(t)
	noIDE(t)
	fakeGoogle(t, map[string]string{"rt1": "oauth@x"})
	d.rememberIDEAccount(ctx, lastSeen("ide@x"))
	_ = d.Store.Set(ctx, "antigravity_refresh_token", "rt1")

	snaps, err := (Antigravity{}).Collect(ctx, d)
	if err != nil || len(snaps) == 0 {
		t.Fatalf("Collect = %d snaps, %v", len(snaps), err)
	}
	accounts := d.Accounts()
	if len(accounts) != 1 || accounts[0].Email != "oauth@x" || !accounts[0].OAuth || accounts[0].Tier != "Pro" {
		t.Fatalf("accounts = %+v", accounts)
	}
}

// TestServiceAccountStaysOnGCP is the regression for gcp_sa_json replacing
// the Antigravity credentials.
func TestServiceAccountStaysOnGCP(t *testing.T) {
	ctx := context.Background()
	d := newTestDeps(t)
	_ = d.Store.Set(ctx, "gcp_sa_json", `{"client_email":"sa@x","private_key":"k"}`)
	_ = d.Store.Set(ctx, "antigravity_refresh_token", "rt1")
	_ = d.Store.Set(ctx, "antigravity_refresh_token_2", "rt2")
	for _, src := range []AuthSource{SourceAntigravity, sourceFor(2)} {
		c, err := d.resolveCreds(ctx, src)
		if err != nil || c.saJSON != "" || c.refreshToken == "" {
			t.Fatalf("%s: creds = %+v, %v", src, c, err)
		}
	}
	if c, _ := d.resolveCreds(ctx, SourceGCP); c.saJSON == "" {
		t.Fatal("gcp lost its service account")
	}
}

// TestDuplicateAccountIsReadOnce covers slot 2 holding the account slot 1
// already shows.
func TestDuplicateAccountIsReadOnce(t *testing.T) {
	ctx := context.Background()
	d := newTestDeps(t)
	noIDE(t)
	calls := fakeGoogle(t, map[string]string{"rt1": "same@x", "rt2": "same@x", "rt3": "other@x"})
	_ = d.Store.Set(ctx, "antigravity_refresh_token", "rt1")
	_ = d.Store.Set(ctx, "antigravity_refresh_token_2", "rt2")
	_ = d.Store.Set(ctx, "antigravity_refresh_token_3", "rt3")

	snaps, err := (Antigravity{}).Collect(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("quota read %d times, want 2 (slots 1 and 3)", calls.Load())
	}
	for _, sn := range snaps {
		if strings.HasSuffix(sn.Metric, "@2") {
			t.Fatalf("slot 2 was read: %s", sn.Metric)
		}
	}
	accounts := d.Accounts()
	if len(accounts) != 3 || accounts[1].DuplicateOf != 1 {
		t.Fatalf("accounts = %+v", accounts)
	}
}

// TestFailedSlotOneStaysOnScreen checks slot 1's last readings survive when
// only the other accounts answer.
func TestFailedSlotOneStaysOnScreen(t *testing.T) {
	ctx := context.Background()
	d := newTestDeps(t)
	noIDE(t)
	fakeGoogle(t, map[string]string{"rt2": "b@x"})
	d.rememberIDEAccount(ctx, lastSeen("ide@x"))
	_ = d.Store.Set(ctx, "antigravity_refresh_token_2", "rt2")
	old := familyCard("gemini", "Gemini models", []model.Series{{Label: "5-hour", Remaining: 40, Total: 100}}, 0)
	if err := d.Store.PutSnapshots(ctx, "antigravity", []model.Snapshot{old}); err != nil {
		t.Fatal(err)
	}

	snaps, err := (Antigravity{}).Collect(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	var kept bool
	for _, sn := range snaps {
		if sn.Metric == "gemini_models" && sn.Status == "error" {
			kept = true
		}
	}
	if !kept {
		t.Fatalf("slot 1 readings dropped: %+v", snaps)
	}
}

func lastSeen(email string) model.Account {
	return model.Account{Slot: 1, Email: email, Primary: true}
}

// startIDE serves GetUserStatus for email with no quota pools, and points hub
// discovery at it.
func startIDE(t *testing.T, email string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == languageServerRPC {
			_, _ = w.Write([]byte(`{"userStatus":{"email":"` + email + `","planStatus":{"planInfo":{"monthlyPromptCredits":10},"availablePromptCredits":5}}}`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	port, _ := strconv.Atoi(strings.TrimPrefix(srv.URL[strings.LastIndex(srv.URL, ":"):], ":"))
	dir := t.TempDir()
	raw, _ := json.Marshal(hubDiscovery{PID: os.Getpid(), HTTPPort: port, CSRFToken: "c"})
	if err := os.WriteFile(filepath.Join(dir, "hub_1.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	orig := hubRoots
	hubRoots = []string{dir}
	t.Cleanup(func() { hubRoots = orig })
}

// TestIDEWithoutPoolsDoesNotBorrowAnotherAccount checks the IDE's card never
// takes the sign-in of a different account: that account gets a slot of its own
// and the IDE's card keeps only what the IDE reported.
func TestIDEWithoutPoolsDoesNotBorrowAnotherAccount(t *testing.T) {
	ctx := context.Background()
	d := newTestDeps(t)
	startIDE(t, "ide@x")
	fakeGoogle(t, map[string]string{"rt1": "other@x"})
	_ = d.Store.Set(ctx, "antigravity_refresh_token", "rt1")

	snaps, err := (Antigravity{}).Collect(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	if d.Setting(ctx, "antigravity_refresh_token") != "" || d.Setting(ctx, "antigravity_refresh_token_2") != "rt1" {
		t.Fatalf("the sign-in did not move off slot 1")
	}
	accounts := d.Accounts()
	if len(accounts) != 2 || accounts[0].Email != "ide@x" || accounts[1].Email != "other@x" {
		t.Fatalf("accounts = %+v", accounts)
	}
	for _, sn := range snaps {
		if sn.Metric == "gemini_models" {
			t.Fatalf("the IDE's card took another account's quota: %+v", sn)
		}
	}

	// Same account: the sign-in stays on slot 1 and stands in for the IDE.
	same := newTestDeps(t)
	startIDE(t, "ide@x")
	calls := fakeGoogle(t, map[string]string{"rt1": "ide@x"})
	_ = same.Store.Set(ctx, "antigravity_refresh_token", "rt1")
	if _, err := (Antigravity{}).Collect(ctx, same); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("fallback not used for the same account")
	}
}

// TestAntigravityIgnoresTheGCPChain checks no google_* setting reaches the
// Antigravity chain: neither the token nor the OAuth client.
func TestAntigravityIgnoresTheGCPChain(t *testing.T) {
	ctx := context.Background()
	d := newTestDeps(t)
	_ = d.Store.Set(ctx, "google_refresh_token", "gcp-rt")
	_ = d.Store.Set(ctx, "google_client_id", "gcp-client")
	_ = d.Store.Set(ctx, "google_client_secret", "gcp-secret")

	if _, err := d.resolveCreds(ctx, SourceAntigravity); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("account 1 used the GCP token: %v", err)
	}
	if d.hasSignIn(ctx, 1) {
		t.Fatal("account 1 reported as signed in through google_refresh_token")
	}

	_ = d.Store.Set(ctx, "antigravity_refresh_token", "rt1")
	c, err := d.resolveCreds(ctx, SourceAntigravity)
	if err != nil || c.clientID != antigravityClientID || c.clientSecret != antigravityClientSecret {
		t.Fatalf("account 1 client = %q, %v; want the Antigravity client", c.clientID, err)
	}
	if c, _ := d.resolveCreds(ctx, SourceGCP); c.refreshToken != "gcp-rt" || c.clientID != "gcp-client" {
		t.Fatalf("GCP chain lost its settings: %+v", c)
	}
}

// fakeIDE serves a language-server mock for one account and points hub
// discovery at it, so the collector sees that account through the IDE.
func fakeIDE(t *testing.T, email string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case languageServerRPC:
			_, _ = w.Write([]byte(`{"userStatus":{"name":"N","email":"` + email +
				`","userTier":{"id":"g1-pro-tier","name":"Pro"}}}`))
		case quotaSummaryRPC:
			_, _ = w.Write([]byte(`{"response":{"groups":[{"displayName":"Gemini Models",
				"buckets":[{"window":"5h","remainingFraction":0.8}]}]}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	raw, _ := json.Marshal(hubDiscovery{PID: os.Getpid(), HTTPPort: port, CSRFToken: "csrf"})
	if err := os.WriteFile(filepath.Join(dir, "hub_1.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	orig := hubRoots
	hubRoots = []string{dir}
	t.Cleanup(func() { hubRoots = orig })
}

// TestSignInFollowsTheAccountWhenTheIDESwitches covers an account that was
// signed in while the IDE used it: when the IDE moves to another account, the
// sign-in moves to its own slot instead of going dark.
func TestSignInFollowsTheAccountWhenTheIDESwitches(t *testing.T) {
	ctx := context.Background()
	d := newTestDeps(t)
	fakeGoogle(t, map[string]string{"rt1": "bob@x"})
	fakeIDE(t, "alice@x")
	_ = d.Store.Set(ctx, "antigravity_refresh_token", "rt1")

	snaps, err := (Antigravity{}).Collect(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	if got := d.Setting(ctx, "antigravity_refresh_token"); got != "" {
		t.Fatalf("slot 1 still holds a sign-in: %q", got)
	}
	if got := d.Setting(ctx, "antigravity_refresh_token_2"); got != "rt1" {
		t.Fatalf("slot 2 token = %q, want the moved sign-in", got)
	}
	accounts := d.Accounts()
	if len(accounts) != 2 || accounts[0].Email != "alice@x" || accounts[0].Slot != 1 {
		t.Fatalf("accounts = %+v", accounts)
	}
	if accounts[1].Email != "bob@x" || accounts[1].Slot != 2 || !accounts[1].OAuth {
		t.Fatalf("account 2 = %+v, want bob over its own sign-in", accounts[1])
	}
	var slot2 bool
	for _, sn := range snaps {
		if sn.Metric == "gemini_models@2" {
			slot2 = true
		}
	}
	if !slot2 {
		t.Fatalf("bob was not read from the cloud: %+v", snaps)
	}
}

// TestSignInStaysOnSlotOneWhileTheIDEUsesIt is the other half: the same account
// on both channels keeps the sign-in where it is, as the IDE's fallback.
func TestSignInStaysOnSlotOneWhileTheIDEUsesIt(t *testing.T) {
	ctx := context.Background()
	d := newTestDeps(t)
	fakeGoogle(t, map[string]string{"rt1": "bob@x"})
	fakeIDE(t, "bob@x")
	_ = d.Store.Set(ctx, "antigravity_refresh_token", "rt1")

	if _, err := (Antigravity{}).Collect(ctx, d); err != nil {
		t.Fatal(err)
	}
	if got := d.Setting(ctx, "antigravity_refresh_token"); got != "rt1" {
		t.Fatalf("slot 1 token = %q, want it left alone", got)
	}
	if got := d.Setting(ctx, "antigravity_refresh_token_2"); got != "" {
		t.Fatalf("slot 2 token = %q, want nothing moved", got)
	}
}

// TestSignInReturnsToSlotOneWhenTheIDESignsInAgain is the case a separate
// account's sign-in runs into: the IDE switches to the account that already had
// a slot of its own. One account, two channels, one row.
func TestSignInReturnsToSlotOneWhenTheIDESignsInAgain(t *testing.T) {
	ctx := context.Background()
	d := newTestDeps(t)
	fakeGoogle(t, map[string]string{"rt2": "bob@x"})
	fakeIDE(t, "bob@x")
	_ = d.Store.Set(ctx, "antigravity_refresh_token_2", "rt2")

	if _, err := (Antigravity{}).Collect(ctx, d); err != nil {
		t.Fatal(err)
	}
	if got := d.Setting(ctx, "antigravity_refresh_token"); got != "rt2" {
		t.Fatalf("slot 1 token = %q, want the sign-in brought back", got)
	}
	if got := d.Setting(ctx, "antigravity_refresh_token_2"); got != "" {
		t.Fatalf("slot 2 still holds a sign-in: %q", got)
	}
	accounts := d.Accounts()
	if len(accounts) != 1 || accounts[0].Slot != 1 || accounts[0].Email != "bob@x" || !accounts[0].OAuth {
		t.Fatalf("accounts = %+v, want bob alone on slot 1 with both channels", accounts)
	}
	if accounts[0].DuplicateOf != 0 {
		t.Fatalf("account 1 reported as a duplicate of itself: %+v", accounts[0])
	}
}

// TestSecondSignInForTheIDEAccountIsDropped: two slots can end up holding the
// same account when it was signed in to Gauge twice, once before the IDE moved
// and once after. Slot 1 is the one that stays.
func TestSecondSignInForTheIDEAccountIsDropped(t *testing.T) {
	ctx := context.Background()
	d := newTestDeps(t)
	fakeGoogle(t, map[string]string{"rt1": "bob@x", "rt2": "bob@x"})
	fakeIDE(t, "bob@x")
	_ = d.Store.Set(ctx, "antigravity_refresh_token", "rt1")
	_ = d.Store.Set(ctx, "antigravity_refresh_token_2", "rt2")

	if _, err := (Antigravity{}).Collect(ctx, d); err != nil {
		t.Fatal(err)
	}
	if got := d.Setting(ctx, "antigravity_refresh_token"); got != "rt1" {
		t.Fatalf("slot 1 token = %q, want it kept", got)
	}
	if got := d.Setting(ctx, "antigravity_refresh_token_2"); got != "" {
		t.Fatalf("slot 2 token = %q, want the copy dropped", got)
	}
	if accounts := d.Accounts(); len(accounts) != 1 || accounts[0].Slot != 1 {
		t.Fatalf("accounts = %+v, want one row", accounts)
	}
}

// TestAdoptSignInLeavesAnotherAccountAlone: the account the IDE is on is not
// on an extra slot, so nothing moves.
func TestAdoptSignInLeavesAnotherAccountAlone(t *testing.T) {
	ctx := context.Background()
	d := newTestDeps(t)
	fakeGoogle(t, map[string]string{"rt2": "carol@x"})
	_ = d.Store.Set(ctx, "antigravity_refresh_token_2", "rt2")

	d.adoptSignIn(ctx, "bob@x")
	if got := d.Setting(ctx, "antigravity_refresh_token"); got != "" {
		t.Fatalf("slot 1 token = %q, want nothing moved", got)
	}
	if got := d.Setting(ctx, "antigravity_refresh_token_2"); got != "rt2" {
		t.Fatalf("slot 2 token = %q, want it left alone", got)
	}
}

// TestIDEClosedDoesNotRepeatAnAccountWithItsOwnSlot: the IDE shutting down is
// not worth a second row for an account another slot already reads.
func TestIDEClosedDoesNotRepeatAnAccountWithItsOwnSlot(t *testing.T) {
	ctx := context.Background()
	d := newTestDeps(t)
	noIDE(t)
	fakeGoogle(t, map[string]string{"rt2": "bob@x"})
	d.rememberIDEAccount(ctx, lastSeen("bob@x"))
	_ = d.Store.Set(ctx, "antigravity_refresh_token_2", "rt2")

	if _, err := (Antigravity{}).Collect(ctx, d); err != nil {
		t.Fatal(err)
	}
	accounts := d.Accounts()
	if len(accounts) != 1 || accounts[0].Slot != 2 || accounts[0].Email != "bob@x" {
		t.Fatalf("accounts = %+v, want only the sign-in's own slot", accounts)
	}
}

// TestPromoteSignInNeedsAFreeSlotLeavesTheFallback: with every extra slot taken
// the sign-in stays on slot 1 as the IDE's fallback.
func TestPromoteSignInNeedsAFreeSlotLeavesTheFallback(t *testing.T) {
	ctx := context.Background()
	d := newTestDeps(t)
	fakeGoogle(t, map[string]string{"rt1": "bob@x"})
	_ = d.Store.Set(ctx, "antigravity_refresh_token", "rt1")
	_ = d.Store.Set(ctx, "antigravity_refresh_token_2", "rt2")
	_ = d.Store.Set(ctx, "antigravity_refresh_token_3", "rt3")

	d.promoteSignIn(ctx, "alice@x")
	if got := d.Setting(ctx, "antigravity_refresh_token"); got != "rt1" {
		t.Fatalf("slot 1 token = %q, want it kept", got)
	}
}

// TestIDEClosedKeepsTheErrorWhenItsOwnSignInFails: dropping slot 1 for an
// account that has its own slot must not turn a failed read into a success,
// which would leave the last numbers on screen looking current.
func TestIDEClosedKeepsTheErrorWhenItsOwnSignInFails(t *testing.T) {
	ctx := context.Background()
	d := newTestDeps(t)
	noIDE(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	origHosts, origUser, origToken := cloudCodeHosts, userInfoEndpoint, tokenEndpoint
	cloudCodeHosts = []string{srv.URL}
	userInfoEndpoint = srv.URL + "/userinfo"
	tokenEndpoint = srv.URL + "/token"
	t.Cleanup(func() { cloudCodeHosts, userInfoEndpoint, tokenEndpoint = origHosts, origUser, origToken })

	d.rememberIDEAccount(ctx, lastSeen("bob@x"))
	_ = d.Store.Set(ctx, "antigravity_refresh_token_2", "rt2")

	if _, err := (Antigravity{}).Collect(ctx, d); err == nil {
		t.Fatal("a failed read reported success; slot 1's last readings would pass for current ones")
	}
}
