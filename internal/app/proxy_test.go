package app

import (
	"net/http"
	"testing"
)

func TestProxySettingRoundTrip(t *testing.T) {
	a := newTestApp(t)

	code, body := do(t, a, http.MethodGet, "/api/proxy", "")
	if code != http.StatusOK {
		t.Fatalf("get proxy: %d", code)
	}
	cfg, _ := body["config"].(map[string]any)
	if cfg == nil {
		t.Fatalf("no config in %v", body)
	}

	if code, _ := do(t, a, http.MethodPost, "/api/settings", `{"proxy_url":"ftp://127.0.0.1:7890"}`); code != http.StatusBadRequest {
		t.Fatalf("bad scheme accepted: %d", code)
	}
	if code, _ := do(t, a, http.MethodPost, "/api/settings", `{"proxy_url":"127.0.0.1:7890"}`); code != http.StatusOK {
		t.Fatalf("save: %d", code)
	}

	code, body = do(t, a, http.MethodGet, "/api/proxy", "")
	if code != http.StatusOK {
		t.Fatalf("get proxy: %d", code)
	}
	cfg, _ = body["config"].(map[string]any)
	if cfg["url"] != "http://127.0.0.1:7890" || cfg["source"] != "setting" {
		t.Fatalf("config after save = %v", cfg)
	}

	if code, _ := do(t, a, http.MethodDelete, "/api/settings/proxy_url", ""); code != http.StatusOK {
		t.Fatalf("clear: %d", code)
	}
	if v, ok := field(t, a, "proxy_url"); ok || v != "" {
		t.Fatalf("cleared field = %q configured=%v", v, ok)
	}

	if code, _ := do(t, a, http.MethodPost, "/api/proxy/test", `{"proxy":"nope://x"}`); code != http.StatusBadRequest {
		t.Fatalf("bad probe accepted: %d", code)
	}
}
