package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestOAuthStartHonoursRedirect checks the desktop shell's override: the
// consent URL must carry the loopback callback the shell listens on, at the
// path Antigravity's client whitelists.
func TestOAuthStartHonoursRedirect(t *testing.T) {
	a := newTestApp(t)
	redirect := "http://127.0.0.1:49152/oauth-callback"
	code, out := do(t, a, http.MethodGet, "/api/oauth/start?slot=2&back=none&redirect="+redirect, "")
	if code != http.StatusOK {
		t.Fatalf("start: %d %v", code, out)
	}
	url, _ := out["url"].(string)
	if !strings.Contains(url, "redirect_uri=http%3A%2F%2F127.0.0.1%3A49152%2Foauth-callback") {
		t.Fatalf("consent url lost the loopback redirect: %s", url)
	}
}

// TestOAuthCallbackRejectsUnknownState covers the branch Google hits when a
// stale link is reopened; it must render a result page without bouncing to a
// route the desktop loopback listener does not serve.
func TestOAuthCallbackRejectsUnknownState(t *testing.T) {
	a := newTestApp(t)
	req := httptest.NewRequest(http.MethodGet, "/oauth-callback?state=stale&code=x", nil)
	rec := httptest.NewRecorder()
	a.Handler.ServeHTTP(rec, req)
	body := rec.Body.String()
	if !strings.Contains(body, "Sign-in failed") {
		t.Fatalf("unexpected callback body: %s", body)
	}
	if strings.Contains(body, `http-equiv="refresh"`) {
		t.Fatalf("desktop callback must not bounce on stale state: %s", body)
	}
}

func TestOAuthCallbackErrorHonoursDesktopNoBounce(t *testing.T) {
	a := newTestApp(t)

	// Desktop loopback error must stay on the result page, not bounce to a 404.
	rec := httptest.NewRecorder()
	a.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/oauth-callback?error=access_denied", nil))
	if body := rec.Body.String(); strings.Contains(body, `http-equiv="refresh"`) || !strings.Contains(body, "cancelled") {
		t.Fatalf("desktop error callback = %s", body)
	}

	// Web error bounces back to the real settings page.
	rec = httptest.NewRecorder()
	a.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/oauth/callback?error=access_denied", nil))
	if body := rec.Body.String(); !strings.Contains(body, `url=/settings.html#google`) {
		t.Fatalf("web error callback did not bounce to settings.html: %s", body)
	}
}

func TestRenderCallbackEscapesMessage(t *testing.T) {
	rec := httptest.NewRecorder()
	renderCallback(rec, false, `<script>alert(1)</script>`, "")
	body := rec.Body.String()
	if strings.Contains(body, "<script>") || !strings.Contains(body, "&lt;script&gt;") {
		t.Fatalf("message was not escaped: %s", body)
	}
}

func TestRenderCallbackBounce(t *testing.T) {
	cases := []struct {
		back    string
		bounces bool
	}{
		{"/settings.html#google", true},
		{"", false},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		renderCallback(rec, true, "ok", c.back)
		got := strings.Contains(rec.Body.String(), `http-equiv="refresh"`)
		if got != c.bounces {
			t.Errorf("back=%q: bounce=%v, want %v", c.back, got, c.bounces)
		}
	}
}
