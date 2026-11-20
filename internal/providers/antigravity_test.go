package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abcdlsj/gauge/internal/model"
)

func TestGroupFamilySeparatesArgon(t *testing.T) {
	for name, want := range map[string]string{
		"Gemini 4 Argon":        "argon",
		"Gemini Models":         "gemini",
		"Claude and GPT models": "claude",
		"Something else":        "other",
	} {
		if got := groupFamily(name); got != want {
			t.Errorf("groupFamily(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestMergeFamiliesKeepsArgonOutOfTheHeadline(t *testing.T) {
	models := []model.Snapshot{{Provider: "antigravity", Metric: "gemini-4-neon-high", Remaining: 96}}
	pools := []poolSeries{
		{family: "gemini", label: "5-hour", remaining: 85},
		{family: "gemini", label: "weekly", remaining: 96},
		{family: "argon", label: "weekly", remaining: 0},
	}
	cards := mergeFamilies(models, pools, nil)
	if len(cards) != 1 || cards[0].Metric != "gemini_models" {
		t.Fatalf("cards = %+v", cards)
	}
	card := cards[0]
	if card.Remaining != 85 {
		t.Fatalf("headline = %v, want the shared 5-hour pool only", card.Remaining)
	}
	if len(card.Series) != 3 {
		t.Fatalf("series = %+v", card.Series)
	}
	argon := card.Series[2]
	if argon.Label != "Argon · weekly" || argon.Remaining != 0 {
		t.Fatalf("argon series = %+v", argon)
	}
}

func TestQuotaSummarySendsTheHubIdentity(t *testing.T) {
	var gotUA, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(map[string]any{"groups": []any{
			map[string]any{
				"displayName": "Gemini Models",
				"buckets": []any{
					map[string]any{"window": "weekly", "remainingFraction": 0.5},
				},
			},
		}})
	}))
	defer srv.Close()

	hosts := cloudCodeHosts
	cloudCodeHosts = []string{srv.URL}
	defer func() { cloudCodeHosts = hosts }()

	d := newTestDeps(t)
	pools := quotaSummary(context.Background(), d, &Auth{Token: "tok"})
	if len(pools) != 1 || pools[0].remaining != 50 {
		t.Fatalf("pools = %+v", pools)
	}
	if gotAuth != "Bearer tok" {
		t.Fatalf("authorization = %q", gotAuth)
	}
	if !strings.HasPrefix(gotUA, "antigravity/hub/") || !strings.Contains(gotUA, "(aidev_client;") {
		t.Fatalf("user agent = %q, want the hub identity", gotUA)
	}
}

func TestHubClientVersionReadsTheDiscoveryFile(t *testing.T) {
	dir := t.TempDir()
	hub := `{"pid":1,"httpPort":1,"httpsPort":2,"lsVersion":"9.9.9","csrfToken":"x"}`
	if err := os.WriteFile(filepath.Join(dir, "hub_1.json"), []byte(hub), 0o600); err != nil {
		t.Fatal(err)
	}
	roots := hubRoots
	hubRoots = []string{dir}
	defer func() { hubRoots = roots }()

	if got := hubClientVersion(); got != "9.9.9" {
		t.Fatalf("hubClientVersion = %q", got)
	}
	ua := antigravityHubUserAgent()
	if !strings.Contains(ua, "/hub/9.9.9 ") || !strings.Contains(ua, "os_type=") || !strings.Contains(ua, "arch=") {
		t.Fatalf("user agent = %q", ua)
	}

	hubRoots = []string{filepath.Join(dir, "missing")}
	if got := hubClientVersion(); got != hubClientFallbackVersion {
		t.Fatalf("fallback = %q", got)
	}
}
