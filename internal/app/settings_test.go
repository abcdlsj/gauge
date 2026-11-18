package app

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func newTestApp(t *testing.T) *App {
	t.Helper()
	dir := t.TempDir()
	a, err := New(context.Background(), Config{
		DBPath:  filepath.Join(dir, "quota.db"),
		KeyPath: filepath.Join(dir, "gauge.key"),
		Logger:  log.New(io.Discard, "", 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	return a
}

func do(t *testing.T, a *App, method, path, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	a.Handler.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

type sectionsReply struct {
	Sections []struct {
		ID     string `json:"id"`
		Fields []struct {
			Key        string `json:"key"`
			Value      string `json:"value"`
			Configured bool   `json:"configured"`
		} `json:"fields"`
	} `json:"sections"`
}

func field(t *testing.T, a *App, key string) (string, bool) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	rec := httptest.NewRecorder()
	a.Handler.ServeHTTP(rec, req)
	var reply sectionsReply
	if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	for _, s := range reply.Sections {
		for _, f := range s.Fields {
			if f.Key == key {
				return f.Value, f.Configured
			}
		}
	}
	t.Fatalf("field %s missing", key)
	return "", false
}

func TestSettingsSaveMaskAndClear(t *testing.T) {
	a := newTestApp(t)

	if v, ok := field(t, a, "refresh_colab_minutes"); ok || v != "30" {
		t.Fatalf("default = %q configured=%v", v, ok)
	}
	if code, _ := do(t, a, http.MethodPost, "/api/settings", `{"refresh_colab_minutes":"45","webhook_url":"https://example.com/hook-abcd","unknown":"x"}`); code != 200 {
		t.Fatalf("save: %d", code)
	}
	if v, _ := field(t, a, "refresh_colab_minutes"); v != "45" {
		t.Fatalf("saved = %q", v)
	}
	if v, ok := field(t, a, "webhook_url"); !ok || v != "••••abcd" {
		t.Fatalf("secret not masked: %q", v)
	}

	if code, _ := do(t, a, http.MethodDelete, "/api/settings/refresh_colab_minutes", ""); code != 200 {
		t.Fatalf("delete: %d", code)
	}
	if v, ok := field(t, a, "refresh_colab_minutes"); ok || v != "30" {
		t.Fatalf("after delete = %q configured=%v", v, ok)
	}
	if code, _ := do(t, a, http.MethodDelete, "/api/settings/accounts_cache", ""); code != 404 {
		t.Fatalf("internal keys must not be deletable, got %d", code)
	}
}

func TestSettingsValidation(t *testing.T) {
	a := newTestApp(t)
	for _, body := range []string{
		`{"refresh_colab_minutes":"10"}`,  // below the floor
		`{"refresh_colab_minutes":"abc"}`, // not a number
		`{"browser_cookies":"firefox"}`,   // not an option
	} {
		code, out := do(t, a, http.MethodPost, "/api/settings", body)
		if code != http.StatusBadRequest || out["error"] == "" {
			t.Fatalf("%s: code %d, %v", body, code, out)
		}
	}
	if _, ok := field(t, a, "refresh_colab_minutes"); ok {
		t.Fatal("a rejected batch must not be stored")
	}
}

func TestIconSetting(t *testing.T) {
	var got []map[string]string
	dir := t.TempDir()
	a, err := New(context.Background(), Config{
		DBPath:           filepath.Join(dir, "quota.db"),
		KeyPath:          filepath.Join(dir, "gauge.key"),
		Logger:           log.New(io.Discard, "", 0),
		OnSettingsChange: func(changed map[string]string) { got = append(got, changed) },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	ctx := context.Background()

	mark := func(path string) string {
		t.Helper()
		rec := httptest.NewRecorder()
		a.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/svg+xml" {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Header().Get("Content-Type"))
		}
		return rec.Body.String()
	}
	gauge, classic := mark("/mark.svg?icon=gauge"), mark("/mark.svg?icon=classic")
	if gauge == classic {
		t.Fatal("the two marks must differ")
	}

	if v, ok := field(t, a, IconSetting); ok || v != IconGauge {
		t.Fatalf("default = %q configured=%v", v, ok)
	}
	if a.Icon(ctx) != IconGauge || mark("/mark.svg") != gauge {
		t.Fatal("default icon must be gauge")
	}

	if code, _ := do(t, a, http.MethodPost, "/api/settings", `{"app_icon":"classic"}`); code != 200 {
		t.Fatalf("save: %d", code)
	}
	if v, _ := field(t, a, IconSetting); v != IconClassic {
		t.Fatalf("saved = %q", v)
	}
	if a.Icon(ctx) != IconClassic || mark("/mark.svg") != classic {
		t.Fatal("/mark.svg must follow the setting")
	}
	if mark("/mark.svg?icon=gauge") != gauge {
		t.Fatal("?icon= must override the setting")
	}
	if code, out := do(t, a, http.MethodPost, "/api/settings", `{"app_icon":"google"}`); code != http.StatusBadRequest || out["key"] != IconSetting {
		t.Fatalf("unknown icon: %d %v", code, out)
	}

	if code, _ := do(t, a, http.MethodDelete, "/api/settings/app_icon", ""); code != 200 {
		t.Fatalf("delete: %d", code)
	}
	if a.Icon(ctx) != IconGauge {
		t.Fatal("clearing must fall back to gauge")
	}

	if len(got) != 2 || got[0][IconSetting] != IconClassic || got[1][IconSetting] != "" {
		t.Fatalf("OnSettingsChange calls = %v", got)
	}
}
