package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/abcdlsj/gauge/internal/model"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	key, err := LoadKey(filepath.Join(dir, "test.key"))
	if err != nil {
		t.Fatal(err)
	}
	st, err := Open(filepath.Join(dir, "test.db"), key)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.InitSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestSecretsAreEncryptedAtRest(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	if err := st.Set(ctx, "google_refresh_token", "1//secret-value"); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := st.DB().QueryRow(`SELECT value FROM kv_store WHERE key = ?`, "google_refresh_token").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if raw == "1//secret-value" || len(raw) < len(encPrefix) {
		t.Fatalf("value stored in clear: %q", raw)
	}
	got, ok, err := st.Get(ctx, "google_refresh_token")
	if err != nil || !ok || got != "1//secret-value" {
		t.Fatalf("roundtrip = %q, %v, %v", got, ok, err)
	}
}

func TestSnapshotUpsertRemovesStaleMetrics(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	now := time.Now()

	batch := []model.Snapshot{
		{Provider: "antigravity", Metric: "a", Label: "A", Remaining: 60, Total: 100, Unit: "%", Status: "ok", UpdatedAt: now},
		{Provider: "antigravity", Metric: "b", Label: "B", Remaining: 30, Total: 100, Unit: "%", Status: "ok", UpdatedAt: now},
	}
	if err := st.PutSnapshots(ctx, "antigravity", batch); err != nil {
		t.Fatal(err)
	}
	if err := st.PutSnapshots(ctx, "antigravity", batch[:1]); err != nil {
		t.Fatal(err)
	}

	snaps, err := st.Snapshots(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 1 || snaps[0].Metric != "a" {
		t.Fatalf("unexpected snapshots: %+v", snaps)
	}
}

func TestEventsAreTrimmed(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	for i := 0; i < 210; i++ {
		if err := st.AddEvent(ctx, "colab", "info", "tick"); err != nil {
			t.Fatal(err)
		}
	}
	events, err := st.Events(ctx, 500)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) > 200 {
		t.Fatalf("event log not trimmed: %d", len(events))
	}
}

// TestInitSchemaDropsRetiredKeys covers the cleanup of settings no release
// reads, and that it spares the keys around them.
func TestInitSchemaDropsRetiredKeys(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	retired := []string{"antigravity_tier", "antigravity_switch", "antigravity_switch_previous_token",
		"antigravity_switch_token_1", "antigravity_switch_token_3"}
	kept := []string{"antigravity_refresh_token_2", "antigravity_account_2", "antigravity_source",
		"antigravity_switchX", "antigravity_ide_account"}
	for _, k := range append(append([]string{}, retired...), kept...) {
		if err := st.Set(ctx, k, "v"); err != nil {
			t.Fatal(err)
		}
	}
	// Run twice: the cleanup must be idempotent.
	for range 2 {
		if err := st.InitSchema(ctx); err != nil {
			t.Fatal(err)
		}
	}
	for _, k := range retired {
		if _, ok, _ := st.Get(ctx, k); ok {
			t.Fatalf("%s survived", k)
		}
	}
	for _, k := range kept {
		if _, ok, _ := st.Get(ctx, k); !ok {
			t.Fatalf("%s was dropped", k)
		}
	}
}
