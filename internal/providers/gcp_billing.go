package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/abcdlsj/gauge/internal/model"
)

// GCPBilling tracks the Google Cloud free-trial / promotional credit balance by
// incrementally summing credit line items from the BigQuery billing export.
//
// The cursor (last successful sync) lives in kv_store, so each query scans at
// most one extra day of partitions and the local accumulator never needs a full
// table scan.
type GCPBilling struct{}

func (GCPBilling) Name() string            { return "gcp-billing" }
func (GCPBilling) Title() string           { return "GCP Billing" }
func (GCPBilling) Interval() time.Duration { return 6 * time.Hour }

var tableNamePattern = regexp.MustCompile(`^[A-Za-z0-9_.:\-]+$`)

const billingQuery = `
SELECT
  IFNULL(SUM(-1 * c.amount), 0) AS incremental_credit_used,
  IFNULL(SUM(cost), 0)          AS incremental_cost,
  IFNULL(ANY_VALUE(currency), '') AS currency
FROM ` + "`%s`" + ` AS b
LEFT JOIN UNNEST(b.credits) AS c
WHERE _PARTITIONTIME >= TIMESTAMP_SUB(@last_sync, INTERVAL 1 DAY)
  AND usage_start_time > @last_sync`

// Collect runs one incremental BigQuery query and derives the remaining credit.
func (GCPBilling) Collect(ctx context.Context, d *Deps) ([]model.Snapshot, error) {
	project := d.Setting(ctx, "gcp_project_id")
	table := d.Setting(ctx, "gcp_billing_table")
	if project == "" || table == "" {
		return nil, fmt.Errorf("%w: set gcp_project_id and gcp_billing_table", ErrNotConfigured)
	}
	if !tableNamePattern.MatchString(table) {
		return nil, fmt.Errorf("%w: gcp_billing_table has invalid characters", ErrNotConfigured)
	}

	initial := d.SettingFloat(ctx, "gcp_initial_credits", 0)
	if initial <= 0 {
		return nil, fmt.Errorf("%w: set gcp_initial_credits", ErrNotConfigured)
	}
	currency := d.SettingDefault(ctx, "gcp_currency", "HKD")

	auth, err := d.GoogleAuth(ctx, []string{
		"https://www.googleapis.com/auth/bigquery.readonly",
		"https://www.googleapis.com/auth/cloud-platform",
	}, SourceGCP)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	cursor := now.AddDate(0, 0, -30)
	if raw := d.Setting(ctx, "gcp_billing_cursor"); raw != "" {
		if t, err := time.Parse(time.RFC3339, raw); err == nil {
			cursor = t
		}
	}

	usedCredits := d.SettingFloat(ctx, "gcp_credit_used", 0)
	usedCost := d.SettingFloat(ctx, "gcp_gross_cost", 0)

	incCredits, incCost, respCurrency, err := queryCredits(ctx, d, project, table, cursor, auth)
	if err != nil {
		return nil, err
	}
	if respCurrency != "" {
		currency = respCurrency
	}

	usedCredits += incCredits
	usedCost += incCost
	if err := d.Store.Set(ctx, "gcp_credit_used", formatFloat(usedCredits)); err != nil {
		d.Log.Printf("gcp-billing: persist used credits: %v", err)
	}
	if err := d.Store.Set(ctx, "gcp_gross_cost", formatFloat(usedCost)); err != nil {
		d.Log.Printf("gcp-billing: persist gross cost: %v", err)
	}
	if err := d.Store.Set(ctx, "gcp_billing_cursor", now.Format(time.RFC3339)); err != nil {
		d.Log.Printf("gcp-billing: persist cursor: %v", err)
	}

	remaining := initial - usedCredits
	if remaining < 0 {
		remaining = 0
	}
	status := model.StatusOK
	if remaining <= 0 {
		status = model.StatusWarning
	}

	out := []model.Snapshot{
		{
			Provider:  "gcp-billing",
			Metric:    "credits_remaining",
			Label:     "Promotional credit balance",
			Remaining: round2(remaining),
			Total:     initial,
			Unit:      currency,
			Status:    status,
			UpdatedAt: now,
		},
		{
			Provider:  "gcp-billing",
			Metric:    "month_to_date_cost",
			Label:     "Usage cost since first sync",
			Remaining: round2(usedCost),
			Unit:      currency,
			Status:    model.StatusOK,
			UpdatedAt: now,
		},
	}
	return out, nil
}

// queryCredits issues the BigQuery jobs.query REST call and parses the single
// aggregate row. Using REST keeps the binary small: no SDK, no gRPC.
func queryCredits(ctx context.Context, d *Deps, project, table string, since time.Time, auth *Auth) (float64, float64, string, error) {
	payload := map[string]any{
		"query":         fmt.Sprintf(billingQuery, table),
		"useLegacySql":  false,
		"parameterMode": "NAMED",
		"queryParameters": []map[string]any{
			{
				"name":          "last_sync",
				"parameterType": map[string]string{"type": "TIMESTAMP"},
				"parameterValue": map[string]string{
					"value": since.Format("2006-01-02 15:04:05.999999+00"),
				},
			},
		},
	}
	body, _ := json.Marshal(payload)
	url := fmt.Sprintf("https://bigquery.googleapis.com/bigquery/v2/projects/%s/queries", project)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, 0, "", err
	}
	req.Header.Set("Authorization", "Bearer "+auth.Token)
	for key, vals := range auth.Header {
		for _, v := range vals {
			req.Header.Add(key, v)
		}
	}
	req.Header.Set("Content-Type", "application/json")

	var resp struct {
		JobComplete bool `json:"jobComplete"`
		Rows        []struct {
			F []struct {
				V string `json:"v"`
			} `json:"f"`
		} `json:"rows"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := doJSON(d.Client, req, &resp); err != nil {
		return 0, 0, "", fmt.Errorf("bigquery: %w", err)
	}
	if len(resp.Errors) > 0 {
		return 0, 0, "", fmt.Errorf("bigquery: %s", resp.Errors[0].Message)
	}
	if len(resp.Rows) == 0 || len(resp.Rows[0].F) < 3 {
		return 0, 0, "", nil
	}
	credits, _ := strconv.ParseFloat(resp.Rows[0].F[0].V, 64)
	cost, _ := strconv.ParseFloat(resp.Rows[0].F[1].V, 64)
	return credits, cost, strings.TrimSpace(resp.Rows[0].F[2].V), nil
}

func formatFloat(v float64) string {
	return strconv.FormatFloat(v, 'f', 6, 64)
}

func round2(v float64) float64 {
	return float64(int64(v*100+0.5)) / 100
}
