package app

import (
	"net/http"
	"testing"
)

func TestUpdateEndpoints(t *testing.T) {
	a := newTestApp(t)

	code, body := do(t, a, http.MethodGet, "/api/update", "")
	if code != http.StatusOK {
		t.Fatalf("GET /api/update: %d", code)
	}
	if body["state"] != "idle" {
		t.Fatalf("expected idle state, got %v", body)
	}
	if body["available"] != false {
		t.Fatalf("expected available=false, got %v", body)
	}

	// Apply when no update is available should return 409 Conflict.
	code, _ = do(t, a, http.MethodPost, "/api/update/apply", "")
	if code != http.StatusConflict {
		t.Fatalf("POST /api/update/apply without update: got %d, want 409", code)
	}
}
