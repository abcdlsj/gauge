package engine

import (
	"context"
	"errors"
	"io"
	"log"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/abcdlsj/gauge/internal/model"
	"github.com/abcdlsj/gauge/internal/providers"
	"github.com/abcdlsj/gauge/internal/store"
)

func TestBackoffDelay(t *testing.T) {
	base := 5 * time.Minute
	genericErr := errors.New("boom")

	if got := backoffDelay(base, 0, nil); got != base {
		t.Fatalf("nil err: got %v, want %v", got, base)
	}
	if got := backoffDelay(base, 3, providers.ErrNotConfigured); got != base {
		t.Fatalf("unconfigured: got %v, want %v", got, base)
	}
	if got := backoffDelay(base, 1, genericErr); got != base {
		t.Fatalf("fail 1: got %v, want %v", got, base)
	}
	if got := backoffDelay(base, 2, genericErr); got != 10*time.Minute {
		t.Fatalf("fail 2: got %v, want 10m", got)
	}
	if got := backoffDelay(base, 4, genericErr); got != 40*time.Minute {
		t.Fatalf("fail 4: got %v, want 40m", got)
	}
	if got := backoffDelay(base, 1, providers.ErrRateLimited); got != 10*time.Minute {
		t.Fatalf("rate limit 1: got %v, want 10m", got)
	}
	if got := backoffDelay(base, 3, providers.ErrRateLimited); got != 40*time.Minute {
		t.Fatalf("rate limit 3: got %v, want 40m", got)
	}
	if got := backoffDelay(base, 1, providers.ErrExpiredAuth); got < time.Hour {
		t.Fatalf("expired auth: got %v, want >= 1h", got)
	}
}

type stubProvider struct {
	name     string
	interval time.Duration
	calls    atomic.Int32
	err      error
}

func (s *stubProvider) Name() string            { return s.name }
func (s *stubProvider) Title() string           { return s.name }
func (s *stubProvider) Interval() time.Duration { return s.interval }
func (s *stubProvider) Collect(ctx context.Context, d *providers.Deps) ([]model.Snapshot, error) {
	s.calls.Add(1)
	if s.err != nil {
		return nil, s.err
	}
	return []model.Snapshot{{
		Provider: s.name,
		Metric:   "m",
		Label:    "M",
		Status:   model.StatusOK,
	}}, nil
}

func TestRefreshStale(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	key := make([]byte, 32)
	st, err := store.Open("file:"+filepath.Join(t.TempDir(), "engine.db")+"?_pragma=journal_mode(WAL)", key)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.InitSchema(ctx); err != nil {
		t.Fatal(err)
	}

	logger := log.New(io.Discard, "", 0)
	deps := providers.NewDeps(st, logger)

	stale := &stubProvider{name: "stale", interval: 5 * time.Minute}
	fresh := &stubProvider{name: "fresh", interval: 5 * time.Minute}
	paused := &stubProvider{name: "paused", interval: 5 * time.Minute, err: providers.ErrExpiredAuth}

	eng := New(st, deps, []providers.Provider{stale, fresh, paused}, logger)
	eng.mu.Lock()
	eng.runCtx = ctx
	eng.lastRun["stale"] = time.Now().Add(-10 * time.Minute)
	eng.prevStatus["stale"] = model.StatusOK
	eng.lastRun["fresh"] = time.Now().Add(-1 * time.Minute)
	eng.prevStatus["fresh"] = model.StatusOK
	eng.lastRun["paused"] = time.Now().Add(-2 * time.Hour)
	eng.prevStatus["paused"] = model.StatusExpiredAuth
	eng.mu.Unlock()

	eng.RefreshStale()

	deadline := time.Now().Add(2 * time.Second)
	for stale.calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if stale.calls.Load() != 1 {
		t.Fatalf("stale calls = %d, want 1", stale.calls.Load())
	}
	if fresh.calls.Load() != 0 {
		t.Fatalf("fresh calls = %d, want 0", fresh.calls.Load())
	}
	if paused.calls.Load() != 0 {
		t.Fatalf("paused calls = %d, want 0", paused.calls.Load())
	}
}
