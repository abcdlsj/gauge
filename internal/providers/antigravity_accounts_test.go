package providers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
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

// TestIDEWithoutPoolsDoesNotBorrowAnotherAccount checks the Cloud Code
// fallback is skipped when Gauge's sign-in is a different account.
func TestIDEWithoutPoolsDoesNotBorrowAnotherAccount(t *testing.T) {
	ctx := context.Background()
	d := newTestDeps(t)
	startIDE(t, "ide@x")
	calls := fakeGoogle(t, map[string]string{"rt1": "other@x"})
	_ = d.Store.Set(ctx, "antigravity_refresh_token", "rt1")

	snaps, err := (Antigravity{}).Collect(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatalf("read quota with another account's token")
	}
	if len(snaps) != 1 || snaps[0].Metric != "other_models" {
		t.Fatalf("want the IDE's credits only, got %+v", snaps)
	}
	if a := d.Accounts(); len(a) != 1 || a[0].Email != "ide@x" {
		t.Fatalf("accounts = %+v", a)
	}

	// Same account: the fallback is allowed.
	calls2 := fakeGoogle(t, map[string]string{"rt1": "ide@x"})
	_ = d.Store.Delete(ctx, identityKey(1))
	if _, err := (Antigravity{}).Collect(ctx, d); err != nil {
		t.Fatal(err)
	}
	if calls2.Load() != 1 {
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
