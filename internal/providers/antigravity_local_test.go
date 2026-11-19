package providers

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/abcdlsj/gauge/internal/store"
)

func newTestDeps(t *testing.T) *Deps {
	t.Helper()
	key := make([]byte, 32)
	st, err := store.Open("file:"+filepath.Join(t.TempDir(), "test.db")+"?_pragma=journal_mode(WAL)", key)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.InitSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	return &Deps{
		Store:  st,
		Client: &http.Client{},
		Log:    log.New(io.Discard, "", 0),
		tokens: make(map[string]cachedToken),
	}
}

func TestAntigravityPrefersLocalIDEOverRemote(t *testing.T) {
	ctx := context.Background()
	d := newTestDeps(t)

	// Configure a remote refresh token so collectRemote WOULD run if not
	// short-circuited by the local IDE.
	if err := d.Store.Set(ctx, "antigravity_refresh_token", "1//fake-token"); err != nil {
		t.Fatal(err)
	}

	// Local IDE language server mock.
	localSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Codeium-Csrf-Token") != "test-csrf" {
			http.Error(w, "bad csrf", http.StatusForbidden)
			return
		}
		switch r.URL.Path {
		case languageServerRPC:
			_, _ = w.Write([]byte(`{
				"userStatus": {
					"name": "Alice",
					"email": "alice@example.com",
					"userTier": {"id": "g1-pro-tier", "name": "Pro"},
					"planStatus": {
						"planInfo": {"monthlyPromptCredits": 500, "monthlyFlowCredits": 100},
						"availablePromptCredits": 400,
						"availableFlowCredits": 80
					}
				}
			}`))
		case quotaSummaryRPC:
			_, _ = w.Write([]byte(`{
				"response": {
					"groups": [
						{"displayName": "Gemini", "buckets": [
							{"window": "5h", "remainingFraction": 0.8},
							{"window": "weekly", "remainingFraction": 0.9}
						]},
						{"displayName": "Claude", "buckets": [
							{"window": "5h", "remainingFraction": 0.6}
						]}
					]
				}
			}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer localSrv.Close()

	u, err := url.Parse(localSrv.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatal(err)
	}

	// Write a hub discovery file pointing at our mock IDE with the current live PID.
	hubDir := t.TempDir()
	hubJSON, _ := json.Marshal(hubDiscovery{
		PID:       os.Getpid(),
		HTTPPort:  port,
		CSRFToken: "test-csrf",
	})
	if err := os.WriteFile(filepath.Join(hubDir, "hub_1.json"), hubJSON, 0o600); err != nil {
		t.Fatal(err)
	}

	origRoots := hubRoots
	hubRoots = []string{hubDir}
	t.Cleanup(func() { hubRoots = origRoots })

	// Point Cloud Code hosts to a trap server; any remote request must fail the test.
	remoteTrap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected remote Cloud Code request: %s %s", r.Method, r.URL.Path)
		http.Error(w, "should not be called", http.StatusInternalServerError)
	}))
	defer remoteTrap.Close()

	origHosts := cloudCodeHosts
	cloudCodeHosts = []string{remoteTrap.URL}
	t.Cleanup(func() { cloudCodeHosts = origHosts })

	snaps, err := (Antigravity{}).Collect(ctx, d)
	if err != nil {
		t.Fatalf("Collect failed: %v", err)
	}
	if len(snaps) < 2 {
		t.Fatalf("expected family snapshots from local IDE, got %d", len(snaps))
	}
	if src := d.Setting(ctx, "antigravity_source"); src != "local_ide" {
		t.Fatalf("antigravity_source = %q, want local_ide", src)
	}
	if tier := d.Setting(ctx, "antigravity_tier"); tier != "g1-pro-tier" {
		t.Fatalf("antigravity_tier = %q, want g1-pro-tier", tier)
	}
}

func TestCallLanguageServerFallsBackToHTTPS(t *testing.T) {
	ctx := context.Background()
	d := newTestDeps(t)

	tlsSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil {
			http.Error(w, "expected TLS", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"userStatus":{"email":"tls@example.com"}}`))
	}))
	defer tlsSrv.Close()

	u, err := url.Parse(tlsSrv.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatal(err)
	}

	hub := hubDiscovery{
		PID:       os.Getpid(),
		HTTPPort:  0,
		HTTPSPort: port,
		CSRFToken: "csrf",
	}
	status, err := fetchLocalStatus(ctx, d, hub)
	if err != nil {
		t.Fatalf("HTTPS fallback failed: %v", err)
	}
	if status.Email != "tls@example.com" {
		t.Fatalf("email = %q, want tls@example.com", status.Email)
	}
}

func TestDiscoverHubSkipsDeadProcess(t *testing.T) {
	hubDir := t.TempDir()
	hubJSON, _ := json.Marshal(hubDiscovery{
		PID:       99999999,
		HTTPPort:  12345,
		CSRFToken: "stale",
	})
	if err := os.WriteFile(filepath.Join(hubDir, "hub_dead.json"), hubJSON, 0o600); err != nil {
		t.Fatal(err)
	}

	origRoots := hubRoots
	hubRoots = []string{hubDir}
	t.Cleanup(func() { hubRoots = origRoots })

	if _, err := discoverHub(); err == nil {
		t.Fatal("expected error when no hub process is alive")
	}
}
