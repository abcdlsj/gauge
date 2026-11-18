package providers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/abcdlsj/gauge/internal/model"
)

// GCPMonitoring reads request counters from Cloud Monitoring to estimate
// Gemini CLI daily usage and paid-tier API traffic.
type GCPMonitoring struct{}

func (GCPMonitoring) Name() string            { return "gcp-monitoring" }
func (GCPMonitoring) Title() string           { return "Cloud Monitoring" }
func (GCPMonitoring) Interval() time.Duration { return 10 * time.Minute }

const (
	monitoringReadScope = "https://www.googleapis.com/auth/monitoring.read"
	requestCountMetric  = "serviceruntime.googleapis.com/api/request_count"
	cliService          = "cloudaicompanion.googleapis.com"
	paidService         = "generativelanguage.googleapis.com"
)

// Collect queries two services: the CLI bucket for "today" and the paid API
// for the last ten minutes.
func (GCPMonitoring) Collect(ctx context.Context, d *Deps) ([]model.Snapshot, error) {
	project := d.Setting(ctx, "gcp_project_id")
	if project == "" {
		return nil, fmt.Errorf("%w: set gcp_project_id", ErrNotConfigured)
	}

	now := time.Now()
	dayStart := pacificMidnight(now)

	auth, err := d.GoogleAuth(ctx, []string{monitoringReadScope}, SourceGCP)
	if err != nil {
		return nil, err
	}

	cliCount, err := countRequests(ctx, d, project, auth, cliService, dayStart, now)
	if err != nil {
		if errors.Is(err, ErrExpiredAuth) {
			d.FlushTokens()
		}
		return nil, err
	}
	recentCount, err := countRequests(ctx, d, project, auth, paidService, now.Add(-10*time.Minute), now)
	if err != nil {
		return nil, err
	}

	reset := dayStart.AddDate(0, 0, 1)

	// Google publishes no daily ceiling for this counter, so the panel reports
	// the count itself instead of inventing a limit to subtract from.
	out := []model.Snapshot{
		{
			Provider:  "gcp-monitoring",
			Metric:    "gemini_cli_requests",
			Label:     "Gemini CLI calls today (PT)",
			Remaining: cliCount,
			Unit:      "calls",
			ResetAt:   &reset,
			Status:    model.StatusOK,
			UpdatedAt: now.UTC(),
		},
		{
			Provider:  "gcp-monitoring",
			Metric:    "paid_api_requests_10m",
			Label:     "Paid API requests · last 10 min",
			Remaining: recentCount,
			Unit:      "req",
			Status:    model.StatusOK,
			UpdatedAt: now.UTC(),
		},
	}
	return out, nil
}

// countRequests sums one metric over a time window with server-side alignment
// and cross-series reduction.
func countRequests(ctx context.Context, d *Deps, project string, auth *Auth, service string, start, end time.Time) (float64, error) {
	window := end.Sub(start)
	if window < time.Minute {
		window = time.Minute
	}
	// Monitoring requires alignment periods that are a multiple of 60s.
	alignSeconds := int64(window.Seconds()/60) * 60
	if alignSeconds < 60 {
		alignSeconds = 60
	}

	q := url.Values{}
	q.Set("filter", fmt.Sprintf(`metric.type="%s" AND resource.type="consumed_api" AND resource.labels.service="%s"`, requestCountMetric, service))
	q.Set("interval.startTime", start.UTC().Format(time.RFC3339))
	q.Set("interval.endTime", end.UTC().Format(time.RFC3339))
	q.Set("aggregation.alignmentPeriod", strconv.FormatInt(alignSeconds, 10)+"s")
	q.Set("aggregation.perSeriesAligner", "ALIGN_SUM")
	q.Set("aggregation.crossSeriesReducer", "REDUCE_SUM")
	q.Set("view", "FULL")

	endpoint := fmt.Sprintf("https://monitoring.googleapis.com/v3/projects/%s/timeSeries?%s", project, q.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+auth.Token)
	for key, vals := range auth.Header {
		for _, v := range vals {
			req.Header.Add(key, v)
		}
	}

	var resp struct {
		TimeSeries []struct {
			Points []struct {
				Value struct {
					Int64Value  string  `json:"int64Value"`
					DoubleValue float64 `json:"doubleValue"`
				} `json:"value"`
			} `json:"points"`
		} `json:"timeSeries"`
	}
	if err := doJSON(d.Client, req, &resp); err != nil {
		return 0, fmt.Errorf("monitoring %s: %w", service, err)
	}

	var total float64
	for _, ts := range resp.TimeSeries {
		for _, p := range ts.Points {
			if p.Value.Int64Value != "" {
				v, _ := strconv.ParseFloat(p.Value.Int64Value, 64)
				total += v
				continue
			}
			total += p.Value.DoubleValue
		}
	}
	return total, nil
}

// pacificMidnight returns 00:00 Pacific for the day containing t. Promo quotas
// reset on Pacific time, so the dashboard mirrors that boundary.
func pacificMidnight(t time.Time) time.Time {
	loc, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		loc = time.FixedZone("PT", -8*3600)
	}
	local := t.In(loc)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
}
