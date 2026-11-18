package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestInstanceLifecycle covers the extra-configuration API and the dynamic
// settings fields the page needs to edit that instance.
func TestInstanceLifecycle(t *testing.T) {
	a := newTestApp(t)

	code, out := do(t, a, http.MethodPost, "/api/instances/commandcode", `{"name":"Work"}`)
	if code != http.StatusOK {
		t.Fatalf("create: %d %v", code, out)
	}
	if slot, ok := out["slot"].(float64); !ok || int(slot) != 2 {
		t.Fatalf("create slot = %v", out["slot"])
	}

	fields := func() map[string]bool {
		t.Helper()
		rec := httptest.NewRecorder()
		a.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/settings", nil))
		var body struct {
			Sections []struct {
				ID     string `json:"id"`
				Fields []struct {
					Key        string `json:"key"`
					Configured bool   `json:"configured"`
				} `json:"fields"`
			} `json:"sections"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		out := map[string]bool{}
		for _, sec := range body.Sections {
			if sec.ID != "commandcode" {
				continue
			}
			for _, f := range sec.Fields {
				out[f.Key] = f.Configured
			}
		}
		return out
	}

	got := fields()
	if _, ok := got["commandcode_account_2"]; !ok {
		t.Fatalf("account name field missing: %v", got)
	}
	if _, ok := got["commandcode_api_key_2"]; !ok {
		t.Fatalf("credential field missing: %v", got)
	}

	if code, _ := do(t, a, http.MethodPost, "/api/settings", `{"commandcode_api_key_2":"cc_live_x"}`); code != http.StatusOK {
		t.Fatalf("save instance key: %d", code)
	}
	if !fields()["commandcode_api_key_2"] {
		t.Fatal("instance key should read back as configured")
	}

	if code, _ := do(t, a, http.MethodDelete, "/api/instances/commandcode/2", ""); code != http.StatusOK {
		t.Fatalf("delete: %d", code)
	}
	if _, ok := fields()["commandcode_api_key_2"]; ok {
		t.Fatal("fields for a removed instance must disappear")
	}
}

func TestInstanceRejectsBadInput(t *testing.T) {
	a := newTestApp(t)
	if code, _ := do(t, a, http.MethodPost, "/api/instances/commandcode", `{"name":"  "}`); code != http.StatusBadRequest {
		t.Fatalf("empty name should be rejected, got %d", code)
	}
	if code, _ := do(t, a, http.MethodPost, "/api/instances/nope", `{"name":"x"}`); code != http.StatusNotFound {
		t.Fatalf("unknown provider should 404, got %d", code)
	}
}
