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
	code, out := do(t, a, http.MethodGet, "/api/oauth/start?slot=2&redirect="+redirect, "")
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
// route the loopback listener does not serve.
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
		t.Fatalf("callback must not bounce on stale state: %s", body)
	}
}

// TestOAuthCallbackErrorStaysPut checks that a cancelled consent stays on the
// result page instead of bouncing to a route the loopback listener lacks.
func TestOAuthCallbackErrorStaysPut(t *testing.T) {
	a := newTestApp(t)
	rec := httptest.NewRecorder()
	a.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/oauth-callback?error=access_denied", nil))
	if body := rec.Body.String(); strings.Contains(body, `http-equiv="refresh"`) || !strings.Contains(body, "cancelled") {
		t.Fatalf("error callback = %s", body)
	}
}

func TestRenderCallbackEscapesMessage(t *testing.T) {
	rec := httptest.NewRecorder()
	renderCallback(rec, false, `<script>alert(1)</script>`)
	body := rec.Body.String()
	if strings.Contains(body, "<script>") || !strings.Contains(body, "&lt;script&gt;") {
		t.Fatalf("message was not escaped: %s", body)
	}
}
