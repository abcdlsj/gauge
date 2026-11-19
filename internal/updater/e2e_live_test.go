//go:build darwin

package updater

import (
	"context"
	"log"
	"os"
	"testing"
	"time"
)

// TestLiveUpdateBootstrapsCopy is a manual end-to-end check of the real update
// path against GitHub: it must be opted into with GAUGE_E2E_UPDATE=1 and
// GAUGE_E2E_TARGET_APP=<throwaway copy of Gauge.app>.
func TestLiveUpdateBootstrapsCopy(t *testing.T) {
	if os.Getenv("GAUGE_E2E_UPDATE") != "1" {
		t.Skip("set GAUGE_E2E_UPDATE=1 to run the live update test")
	}
	target := os.Getenv("GAUGE_E2E_TARGET_APP")
	if target == "" {
		t.Fatal("GAUGE_E2E_TARGET_APP is required")
	}

	m := New(Config{
		CurrentVersion: "0.0.0",
		CacheDir:       t.TempDir(),
		Logger:         log.New(os.Stderr, "e2e: ", 0),
		EnableApply:    true,
		OnQuit:         func() { t.Log("onQuit fired; helper takes over when this process exits") },
		detectAppFn:    func() (string, bool) { return target, true },
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	st := m.Check(ctx)
	t.Logf("check: %+v", st)
	if !st.Available || !st.CanApply {
		t.Fatalf("no applicable update: %+v", st)
	}

	if err := m.Apply(); err != nil {
		t.Fatalf("apply: %v", err)
	}
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		st = m.Status()
		t.Logf("apply: state=%s progress=%d err=%q", st.State, st.Progress, st.Error)
		if st.State == StateInstalling || st.State == StateError {
			break
		}
		time.Sleep(2 * time.Second)
	}
	if st.State != StateInstalling {
		t.Fatalf("apply did not reach installing: %+v", st)
	}
}
