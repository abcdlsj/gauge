// Package model holds the types shared by the storage layer, the collectors
// and the HTTP API. Keeping them in a leaf package avoids import cycles.
package model

import "time"

// Provider status values stored on snapshots and runs.
const (
	StatusOK           = "ok"
	StatusWarning      = "warning"
	StatusError        = "error"
	StatusUnconfigured = "unconfigured"
	StatusExpiredAuth  = "expired_auth"
	StatusRunning      = "running"
)

// Series is one window of a quota (5-hour rolling, weekly, ...). Cards with more
// than one window render one bar per series.
type Series struct {
	Label     string     `json:"label"`
	Remaining float64    `json:"remaining"`
	Total     float64    `json:"total"`
	Unit      string     `json:"unit,omitempty"`
	ResetAt   *time.Time `json:"reset_at,omitempty"`
}

// Account is one signed-in Google identity the panel collects for.
type Account struct {
	Slot    int    `json:"slot"` // 1 = the IDE's account, 2+ = Gauge sign-ins only
	Email   string `json:"email"`
	Name    string `json:"name,omitempty"`
	Tier    string `json:"tier,omitempty"`    // readable plan name
	TierID  string `json:"tier_id,omitempty"` // raw id, kept internal
	Picture string `json:"picture,omitempty"` // data URI or https URL
	Primary bool   `json:"primary"`
	// OAuth is set when Gauge holds its own sign-in for this slot.
	OAuth bool `json:"oauth,omitempty"`
	// DuplicateOf names the earlier slot already showing this account; its
	// quota is not read twice.
	DuplicateOf int `json:"duplicate_of,omitempty"`
	// IDEEmail (slot 1 only) is the account the IDE is, or was last, signed
	// in to; SignInEmail is the account of Gauge's own sign-in. They differ
	// when the two were signed in with different accounts.
	IDEEmail    string `json:"ide_email,omitempty"`
	SignInEmail string `json:"sign_in_email,omitempty"`
}

// Snapshot is a single quota reading for one provider metric.
// Remaining and Total share Unit; a Total of zero means "metered, no ceiling".
type Snapshot struct {
	Provider  string     `json:"provider"`
	Metric    string     `json:"metric"`
	Label     string     `json:"label"`
	Remaining float64    `json:"remaining"`
	Total     float64    `json:"total"`
	Unit      string     `json:"unit"`
	Note      string     `json:"note,omitempty"`
	Kind      string     `json:"kind,omitempty"` // empty = metric, "note" = text-only card
	Account   string     `json:"account,omitempty"`
	Series    []Series   `json:"series,omitempty"`
	ResetAt   *time.Time `json:"reset_at,omitempty"`
	Status    string     `json:"status"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// RunState is the last known scheduling state of one provider loop.
type RunState struct {
	Provider   string     `json:"provider"`
	Status     string     `json:"status"`
	Error      string     `json:"error,omitempty"`
	LastRun    *time.Time `json:"last_run,omitempty"`
	NextRun    *time.Time `json:"next_run,omitempty"`
	DurationMS int64      `json:"duration_ms"`
}

// Event is a single line in the activity log surfaced by the dashboard.
type Event struct {
	ID       int64     `json:"id"`
	Time     time.Time `json:"time"`
	Provider string    `json:"provider"`
	Level    string    `json:"level"`
	Message  string    `json:"message"`
}
