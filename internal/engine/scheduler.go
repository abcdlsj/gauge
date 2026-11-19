package engine

import (
	"context"
	"errors"
	"log"
	"math/rand/v2"
	"sort"
	"sync"
	"time"

	"github.com/abcdlsj/gauge/internal/model"
	"github.com/abcdlsj/gauge/internal/providers"
	"github.com/abcdlsj/gauge/internal/store"
)

// Errors surfaced to the HTTP layer.
var (
	ErrCooldown        = errors.New("refresh cooldown active")
	ErrUnknownProvider = errors.New("unknown provider")
)

// manualCooldown keeps repeated clicks from hammering upstream APIs.
const manualCooldown = 60 * time.Second

// Engine owns the provider loops, the run bookkeeping and the alert hook.
type Engine struct {
	store     *store.Store
	deps      *providers.Deps
	providers []providers.Provider
	log       *log.Logger

	mu         sync.Mutex
	runCtx     context.Context
	lastRun    map[string]time.Time
	inFlight   map[string]bool
	prevStatus map[string]string // provider -> last observed run status
	started    time.Time
}

// New wires an engine around the given store and provider list.
func New(st *store.Store, deps *providers.Deps, list []providers.Provider, logger *log.Logger) *Engine {
	return &Engine{
		store:      st,
		deps:       deps,
		providers:  list,
		log:        logger,
		lastRun:    make(map[string]time.Time),
		inFlight:   make(map[string]bool),
		prevStatus: make(map[string]string),
		started:    time.Now(),
	}
}

// ProviderMeta is the static description of a provider for the dashboard.
type ProviderMeta struct {
	Name            string `json:"name"`
	Title           string `json:"title"`
	IntervalSeconds int    `json:"interval_seconds"`
}

// Overview is the single payload the frontend polls.
type Overview struct {
	GeneratedAt time.Time            `json:"generated_at"`
	StartedAt   time.Time            `json:"started_at"`
	Providers   []ProviderMeta       `json:"providers"`
	Quotas      []model.Snapshot     `json:"quotas"`
	Runs        []model.RunState     `json:"runs"`
	Events      []model.Event        `json:"events"`
	Accounts    []model.Account      `json:"accounts"`
	Settings    map[string]bool      `json:"settings"`
	Cooldown    map[string]time.Time `json:"cooldown"`
	// Layout is the subscription groups and their cards, in the user's
	// order, with the visibility the menu bar and the overview apply.
	Layout []LayoutGroup `json:"layout"`
}

// Start launches one goroutine per provider. Each loop collects immediately,
// then sleeps for its interval plus a small jitter.
func (e *Engine) Start(ctx context.Context) {
	e.mu.Lock()
	e.runCtx = ctx
	e.mu.Unlock()
	e.hydrate(ctx)
	for _, p := range e.providers {
		go e.loop(ctx, p)
	}
}

// Trigger schedules an immediate background collection for the named providers
// (or all providers when names is empty). It is a no-op before Start is called,
// which keeps offline HTTP tests from hitting upstream networks.
func (e *Engine) Trigger(names ...string) {
	e.mu.Lock()
	ctx := e.runCtx
	e.mu.Unlock()
	if ctx == nil || ctx.Err() != nil {
		return
	}
	if len(names) == 0 {
		for _, p := range e.providers {
			name := p.Name()
			go func() { _, _ = e.RunOnce(ctx, name, false) }()
		}
		return
	}
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		if seen[name] {
			continue
		}
		seen[name] = true
		n := name
		go func() { _, _ = e.RunOnce(ctx, n, false) }()
	}
}

// hydrate restores the last run timestamps so the cooldown survives restarts.
func (e *Engine) hydrate(ctx context.Context) {
	runs, err := e.store.Runs(ctx)
	if err != nil {
		e.log.Printf("engine: load runs: %v", err)
		return
	}
	for _, r := range runs {
		if r.LastRun != nil {
			e.lastRun[r.Provider] = *r.LastRun
		}
		e.prevStatus[r.Provider] = r.Status
	}
}

func (e *Engine) loop(ctx context.Context, p providers.Provider) {
	delay := time.Duration(0)
	for {
		if delay > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
		}
		if ctx.Err() != nil {
			return
		}
		if _, err := e.RunOnce(ctx, p.Name(), false); err != nil && !errors.Is(err, ErrCooldown) {
			e.log.Printf("[%s] collect: %v", p.Name(), err)
		}
		interval := e.deps.IntervalOverride(ctx, p.Name(), p.Interval())
		delay = interval + jitterFor(p.Name(), interval)
	}
}

// jitterFor desynchronises polling. Colab gets the wider 1–5 minute window the
// upstream endpoint expects; everything else drifts by up to 5%.
func jitterFor(name string, interval time.Duration) time.Duration {
	if name == "colab" {
		return time.Minute + time.Duration(rand.Int64N(int64(4*time.Minute)))
	}
	spread := interval / 20
	if spread <= 0 {
		return 0
	}
	return time.Duration(rand.Int64N(int64(spread)))
}

// RunOnce collects one provider. Manual calls respect the cooldown; scheduled
// calls bypass it. The returned RunState reflects the attempt.
func (e *Engine) RunOnce(ctx context.Context, name string, manual bool) (model.RunState, error) {
	prov, ok := e.find(name)
	if !ok {
		return model.RunState{Provider: name, Status: model.StatusError, Error: ErrUnknownProvider.Error()}, ErrUnknownProvider
	}

	e.mu.Lock()
	if e.inFlight[name] {
		e.mu.Unlock()
		return model.RunState{Provider: name, Status: model.StatusRunning}, ErrCooldown
	}
	if manual {
		if last, ok := e.lastRun[name]; ok && time.Since(last) < manualCooldown {
			e.mu.Unlock()
			return model.RunState{Provider: name, Status: model.StatusRunning}, ErrCooldown
		}
	}
	e.inFlight[name] = true
	e.lastRun[name] = time.Now()
	e.mu.Unlock()

	defer func() {
		e.mu.Lock()
		delete(e.inFlight, name)
		e.mu.Unlock()
	}()

	runCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()

	started := time.Now()
	runs := model.RunState{Provider: name, Status: model.StatusRunning, LastRun: &started}
	next := started.Add(e.deps.IntervalOverride(ctx, name, prov.Interval()))
	runs.NextRun = &next
	_ = e.store.PutRun(ctx, runs)

	snaps, err := prov.Collect(runCtx, e.deps)
	duration := time.Since(started)

	finished := time.Now()
	runs.LastRun = &finished
	runs.DurationMS = duration.Milliseconds()
	next = finished.Add(e.deps.IntervalOverride(ctx, name, prov.Interval()))
	runs.NextRun = &next

	if err != nil {
		runs.Status = classify(err)
		runs.Error = err.Error()
		// A missing credential leaves old readings intact; they are simply
		// stale. Hard failures flag the stored rows so the UI cannot mistake
		// them for current numbers.
		if runs.Status != model.StatusUnconfigured {
			_ = e.store.MarkProviderStatus(ctx, name, runs.Status)
		}
	} else {
		runs.Status = model.StatusOK
		runs.Error = ""
		for i := range snaps {
			if snaps[i].UpdatedAt.IsZero() {
				snaps[i].UpdatedAt = finished
			}
			if snaps[i].Status == "" {
				snaps[i].Status = model.StatusOK
			}
		}
		if err := e.store.PutSnapshots(ctx, name, snaps); err != nil {
			runs.Status = model.StatusError
			runs.Error = err.Error()
			e.log.Printf("[%s] persist snapshots: %v", name, err)
		}
	}

	_ = e.store.PutRun(ctx, runs)
	e.observeStatus(ctx, name, runs, snaps)
	return runs, err
}

// observeStatus emits an activity event (and a webhook, when configured) only
// when a provider's state actually changes, so the log stays readable.
func (e *Engine) observeStatus(ctx context.Context, name string, run model.RunState, snaps []model.Snapshot) {
	e.mu.Lock()
	prev, known := e.prevStatus[name]
	e.prevStatus[name] = run.Status
	e.mu.Unlock()

	if !known || prev == run.Status {
		return
	}
	level := "info"
	message := ""
	switch run.Status {
	case model.StatusOK:
		level = "info"
		message = "recovered"
	case model.StatusUnconfigured:
		level = "info"
		message = "waiting for credentials"
	case model.StatusExpiredAuth:
		level = "error"
		message = "credentials rejected, update them in settings"
	default:
		level = "error"
		message = run.Error
	}
	if message == "" {
		message = run.Status
	}
	_ = e.store.AddEvent(ctx, name, level, message)
	if level == "error" {
		e.notifyWebhook(ctx, name, message)
	}

	for _, sn := range snaps {
		if sn.Status == model.StatusWarning {
			_ = e.store.AddEvent(ctx, name, "warning", sn.Label+" is running low")
			e.notifyWebhook(ctx, name, sn.Label+" is running low")
			break
		}
	}
}

// classify maps a collector error onto a user-visible status.
func classify(err error) string {
	switch {
	case errors.Is(err, providers.ErrNotConfigured):
		return model.StatusUnconfigured
	case errors.Is(err, providers.ErrExpiredAuth):
		return model.StatusExpiredAuth
	default:
		return model.StatusError
	}
}

func (e *Engine) find(name string) (providers.Provider, bool) {
	for _, p := range e.providers {
		if p.Name() == name {
			return p, true
		}
	}
	return nil, false
}

// ResetCooldown clears the manual-refresh guard, so saving credentials is
// immediately followed by a usable Refresh button.
func (e *Engine) ResetCooldown(names ...string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(names) == 0 {
		e.lastRun = make(map[string]time.Time)
		return
	}
	for _, n := range names {
		delete(e.lastRun, n)
	}
}

// RefreshAll triggers every provider, skipping ones still cooling down or
// already running. It returns the per-provider outcome.
func (e *Engine) RefreshAll(ctx context.Context) []model.RunState {
	type result struct {
		idx int
		run model.RunState
	}
	ch := make(chan result, len(e.providers))
	var wg sync.WaitGroup
	for i, p := range e.providers {
		wg.Add(1)
		go func(i int, name string) {
			defer wg.Done()
			run, _ := e.RunOnce(ctx, name, true)
			ch <- result{idx: i, run: run}
		}(i, p.Name())
	}
	wg.Wait()
	close(ch)

	out := make([]model.RunState, 0, len(e.providers))
	for r := range ch {
		out = append(out, r.run)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Provider < out[j].Provider })
	return out
}

// Accounts returns the cached identities for the avatar handler.
func (e *Engine) Accounts() []model.Account { return e.deps.Accounts() }

// Overview assembles the dashboard payload from local data only; it never
// touches an upstream API, which keeps page loads in the millisecond range.
func (e *Engine) Overview(ctx context.Context) (Overview, error) {
	quotas, err := e.store.Snapshots(ctx)
	if err != nil {
		return Overview{}, err
	}
	runs, err := e.store.Runs(ctx)
	if err != nil {
		return Overview{}, err
	}
	events, err := e.store.Events(ctx, 40)
	if err != nil {
		return Overview{}, err
	}
	keys, err := e.store.Keys(ctx)
	if err != nil {
		return Overview{}, err
	}

	accounts := e.deps.Accounts()

	meta := make([]ProviderMeta, 0, len(e.providers))
	for _, p := range e.providers {
		meta = append(meta, ProviderMeta{
			Name:            p.Name(),
			Title:           p.Title(),
			IntervalSeconds: int(p.Interval().Seconds()),
		})
	}
	sort.Slice(meta, func(i, j int) bool { return meta[i].Name < meta[j].Name })

	cooldown := make(map[string]time.Time, len(e.providers))
	e.mu.Lock()
	for _, p := range e.providers {
		if last, ok := e.lastRun[p.Name()]; ok {
			cooldown[p.Name()] = last.Add(manualCooldown)
		}
	}
	e.mu.Unlock()

	settings := make(map[string]bool, len(keys))
	for _, k := range keys {
		settings[k] = true
	}

	return Overview{
		GeneratedAt: time.Now(),
		StartedAt:   e.started,
		Providers:   meta,
		Quotas:      quotas,
		Runs:        runs,
		Events:      events,
		Accounts:    accounts,
		Settings:    settings,
		Cooldown:    cooldown,
		Layout:      e.Layout(ctx, quotas, accounts),
	}, nil
}

// notifyWebhook posts an alert to the configured webhook (Slack, Discord or any
// endpoint that accepts JSON). Failures are logged, never retried.
func (e *Engine) notifyWebhook(ctx context.Context, provider, message string) {
	url, ok, err := e.store.Get(ctx, "webhook_url")
	if err != nil || !ok || url == "" {
		return
	}
	payload := map[string]string{
		"text":     "Gauge · " + provider + ": " + message,
		"provider": provider,
		"message":  message,
		"source":   "gauge",
	}
	go func() {
		reqCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := postJSON(reqCtx, e.deps.Client, url, payload); err != nil {
			e.log.Printf("webhook: %v", err)
		}
	}()
}
