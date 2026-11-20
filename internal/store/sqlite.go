// Package store owns the embedded SQLite database: quota snapshots, provider
// run state, an activity log and a small key/value table for credentials.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/abcdlsj/gauge/internal/model"

	_ "modernc.org/sqlite"
)

// Store is a thin, synchronous wrapper around the SQLite handle. WAL mode plus
// a single writer goroutine (the engine) keeps concurrent reads cheap.
type Store struct {
	db  *sql.DB
	box *box
}

// Open opens (and creates if needed) the database file.
func Open(path string, key []byte) (*Store, error) {
	b, err := newBox(key)
	if err != nil {
		return nil, err
	}
	dsn := path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(1)"
	if strings.HasPrefix(path, "file:") {
		dsn = path
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	return &Store{db: db, box: b}, nil
}

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

// DB exposes the raw handle for maintenance tasks.
func (s *Store) DB() *sql.DB { return s.db }

// InitSchema creates every table used by the panel. It is idempotent.
func (s *Store) InitSchema(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS quota_snapshots (
	provider   TEXT NOT NULL,
	metric     TEXT NOT NULL,
	label      TEXT NOT NULL,
	remaining  REAL NOT NULL DEFAULT 0,
	total      REAL NOT NULL DEFAULT 0,
	unit       TEXT NOT NULL DEFAULT '',
	note       TEXT NOT NULL DEFAULT '',
	kind       TEXT NOT NULL DEFAULT '',
	account    TEXT NOT NULL DEFAULT '',
	series     TEXT NOT NULL DEFAULT '',
	reset_at   INTEGER,
	status     TEXT NOT NULL DEFAULT 'ok',
	updated_at INTEGER NOT NULL,
	PRIMARY KEY (provider, metric)
);
CREATE TABLE IF NOT EXISTS provider_runs (
	provider    TEXT PRIMARY KEY,
	status      TEXT NOT NULL DEFAULT 'idle',
	error       TEXT NOT NULL DEFAULT '',
	last_run    INTEGER,
	next_run    INTEGER,
	duration_ms INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS events (
	id       INTEGER PRIMARY KEY AUTOINCREMENT,
	ts       INTEGER NOT NULL,
	provider TEXT NOT NULL,
	level    TEXT NOT NULL,
	message  TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS kv_store (
	key        TEXT PRIMARY KEY,
	value      TEXT NOT NULL,
	updated_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_events_ts ON events(ts DESC);
`)
	if err != nil {
		return err
	}
	// Upgrade databases created before the note column existed.
	_, _ = s.db.ExecContext(ctx, `ALTER TABLE quota_snapshots ADD COLUMN note TEXT NOT NULL DEFAULT ''`)
	_, _ = s.db.ExecContext(ctx, `ALTER TABLE quota_snapshots ADD COLUMN kind TEXT NOT NULL DEFAULT ''`)
	_, _ = s.db.ExecContext(ctx, `ALTER TABLE quota_snapshots ADD COLUMN account TEXT NOT NULL DEFAULT ''`)
	_, _ = s.db.ExecContext(ctx, `ALTER TABLE quota_snapshots ADD COLUMN series TEXT NOT NULL DEFAULT ''`)
	return s.dropRetiredKeys(ctx)
}

// retiredKeys are settings no release reads any more. Dropping them keeps a
// stale value from coming back if a later key reuses the name.
var retiredKeys = []string{
	// The slot-1 plan, overwritten by every extra account and never read.
	"antigravity_tier",
	// The account switch, which never shipped: its setting, the token it
	// replaced, and the per-identity copies.
	"antigravity_switch",
	"antigravity_switch_previous_token",
}

// retiredKeyPatterns are LIKE patterns for retired keys that carry a slot.
var retiredKeyPatterns = []string{
	`antigravity\_switch\_token\_%`,
}

// dropRetiredKeys removes retiredKeys and retiredKeyPatterns. It is
// idempotent, so it runs on every start.
func (s *Store) dropRetiredKeys(ctx context.Context) error {
	query := `DELETE FROM kv_store WHERE key IN (` + placeholders(len(retiredKeys)) + `)`
	args := make([]any, 0, len(retiredKeys))
	for _, k := range retiredKeys {
		args = append(args, k)
	}
	if _, err := s.db.ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("drop retired settings: %w", err)
	}
	for _, pattern := range retiredKeyPatterns {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM kv_store WHERE key LIKE ? ESCAPE '\'`, pattern); err != nil {
			return fmt.Errorf("drop retired settings: %w", err)
		}
	}
	return nil
}

// PutSnapshots upserts one provider's readings in a single transaction and
// drops rows the provider no longer reports.
func (s *Store) PutSnapshots(ctx context.Context, provider string, snaps []model.Snapshot) error {
	if len(snaps) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.PrepareContext(ctx, `
INSERT INTO quota_snapshots (provider, metric, label, remaining, total, unit, note, kind, account, series, reset_at, status, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(provider, metric) DO UPDATE SET
	label=excluded.label, remaining=excluded.remaining, total=excluded.total,
	unit=excluded.unit, note=excluded.note, kind=excluded.kind, account=excluded.account,
	series=excluded.series, reset_at=excluded.reset_at, status=excluded.status,
	updated_at=excluded.updated_at`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	keep := make([]any, 0, len(snaps)+1)
	keep = append(keep, provider)
	var reset any
	for _, sn := range snaps {
		reset = nil
		if sn.ResetAt != nil {
			reset = sn.ResetAt.Unix()
		}
		series := ""
		if len(sn.Series) > 0 {
			if raw, err := json.Marshal(sn.Series); err == nil {
				series = string(raw)
			}
		}
		if _, err := stmt.ExecContext(ctx, sn.Provider, sn.Metric, sn.Label, sn.Remaining, sn.Total,
			sn.Unit, sn.Note, sn.Kind, sn.Account, series, reset, sn.Status, sn.UpdatedAt.Unix()); err != nil {
			return err
		}
		keep = append(keep, sn.Metric)
	}

	query := fmt.Sprintf(`DELETE FROM quota_snapshots WHERE provider = ? AND metric NOT IN (%s)`,
		placeholders(len(snaps)))
	if _, err := tx.ExecContext(ctx, query, keep...); err != nil {
		return err
	}
	return tx.Commit()
}

func placeholders(n int) string {
	if n <= 0 {
		return "''"
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// MarkProviderStatus rewrites the status of every snapshot of a provider, used
// when a collection fails so stale numbers are visibly flagged.
func (s *Store) MarkProviderStatus(ctx context.Context, provider, status string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE quota_snapshots SET status = ? WHERE provider = ?`, status, provider)
	return err
}

// Snapshots returns every stored reading ordered for a stable UI.
func (s *Store) Snapshots(ctx context.Context) ([]model.Snapshot, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT provider, metric, label, remaining, total, unit, note, kind, account, series, reset_at, status, updated_at
FROM quota_snapshots ORDER BY provider, label, metric`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]model.Snapshot, 0, 16)
	for rows.Next() {
		var (
			sn      model.Snapshot
			series  string
			reset   sql.NullInt64
			updated int64
		)
		if err := rows.Scan(&sn.Provider, &sn.Metric, &sn.Label, &sn.Remaining, &sn.Total,
			&sn.Unit, &sn.Note, &sn.Kind, &sn.Account, &series, &reset, &sn.Status, &updated); err != nil {
			return nil, err
		}
		if series != "" {
			_ = json.Unmarshal([]byte(series), &sn.Series)
		}
		if reset.Valid {
			t := time.Unix(reset.Int64, 0)
			sn.ResetAt = &t
		}
		sn.UpdatedAt = time.Unix(updated, 0)
		out = append(out, sn)
	}
	return out, rows.Err()
}

// PutRun records the scheduling state of a provider loop.
func (s *Store) PutRun(ctx context.Context, r model.RunState) error {
	var last, next any
	if r.LastRun != nil {
		last = r.LastRun.Unix()
	}
	if r.NextRun != nil {
		next = r.NextRun.Unix()
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO provider_runs (provider, status, error, last_run, next_run, duration_ms)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(provider) DO UPDATE SET
	status=excluded.status, error=excluded.error, last_run=excluded.last_run,
	next_run=excluded.next_run, duration_ms=excluded.duration_ms`,
		r.Provider, r.Status, r.Error, last, next, r.DurationMS)
	return err
}

// Runs returns every provider run row.
func (s *Store) Runs(ctx context.Context) ([]model.RunState, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT provider, status, error, last_run, next_run, duration_ms FROM provider_runs ORDER BY provider`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]model.RunState, 0, 4)
	for rows.Next() {
		var (
			r          model.RunState
			last, next sql.NullInt64
		)
		if err := rows.Scan(&r.Provider, &r.Status, &r.Error, &last, &next, &r.DurationMS); err != nil {
			return nil, err
		}
		if last.Valid {
			t := time.Unix(last.Int64, 0)
			r.LastRun = &t
		}
		if next.Valid {
			t := time.Unix(next.Int64, 0)
			r.NextRun = &t
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// AddEvent appends to the activity log and trims it to the newest 200 lines.
func (s *Store) AddEvent(ctx context.Context, provider, level, message string) error {
	now := time.Now().Unix()
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO events (ts, provider, level, message) VALUES (?, ?, ?, ?)`,
		now, provider, level, message); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM events WHERE id <= (SELECT MAX(id) - 200 FROM events)`)
	return err
}

// Events returns the newest events first.
func (s *Store) Events(ctx context.Context, limit int) ([]model.Event, error) {
	if limit <= 0 || limit > 200 {
		limit = 40
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, ts, provider, level, message FROM events ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]model.Event, 0, limit)
	for rows.Next() {
		var (
			e  model.Event
			ts int64
		)
		if err := rows.Scan(&e.ID, &ts, &e.Provider, &e.Level, &e.Message); err != nil {
			return nil, err
		}
		e.Time = time.Unix(ts, 0)
		out = append(out, e)
	}
	return out, rows.Err()
}

// Get returns a settings value, transparently decrypting secrets.
func (s *Store) Get(ctx context.Context, key string) (string, bool, error) {
	var stored string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM kv_store WHERE key = ?`, key).Scan(&stored)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	plain, err := s.box.open(stored)
	if err != nil {
		return "", false, fmt.Errorf("decrypt %s: %w", key, err)
	}
	return plain, true, nil
}

// Set writes a settings value, encrypting secret-looking keys on the way in.
func (s *Store) Set(ctx context.Context, key, value string) error {
	payload := value
	if sensitive(key) && value != "" {
		sealed, err := s.box.seal(value)
		if err != nil {
			return err
		}
		payload = sealed
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO kv_store (key, value, updated_at) VALUES (?, ?, ?)
ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at`,
		key, payload, time.Now().Unix())
	return err
}

// Keys lists every settings key without touching secret material.
func (s *Store) Keys(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key FROM kv_store ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// Delete removes a settings key (used to clear paused provider flags).
func (s *Store) Delete(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM kv_store WHERE key = ?`, key)
	return err
}

// GetInt reads an integer cursor, returning def when unset or unparsable.
func (s *Store) GetInt(ctx context.Context, key string, def int64) int64 {
	raw, ok, err := s.Get(ctx, key)
	if err != nil || !ok || raw == "" {
		return def
	}
	var v int64
	if _, err := fmt.Sscanf(raw, "%d", &v); err != nil {
		return def
	}
	return v
}
