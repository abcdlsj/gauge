package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/abcdlsj/gauge/internal/engine"
	"github.com/abcdlsj/gauge/internal/model"
)

// TestLayoutSplitsGoogleAccounts checks that every signed-in identity is its
// own subscription row: its own title, avatar fields and cards.
func TestLayoutSplitsGoogleAccounts(t *testing.T) {
	a := newTestApp(t)
	ctx := context.Background()
	snap := func(metric string) model.Snapshot {
		return model.Snapshot{Provider: "antigravity", Metric: metric, Label: metric, Status: "ok", UpdatedAt: time.Now()}
	}
	quotas := []model.Snapshot{
		snap("gemini_models"),
		snap("gemini_models@2"),
		{Provider: "colab", Metric: "compute_units", Label: "Colab", Status: "ok", UpdatedAt: time.Now()},
	}
	accounts := []model.Account{
		{Slot: 1, Email: "a@x", Name: "A", Tier: "Ultra 5x"},
		{Slot: 2, Email: "b@x", Name: "B", Tier: "Pro"},
	}

	groups := a.Engine.Layout(ctx, quotas, accounts)
	if len(groups) < 2 || groups[0].ID != "google" || groups[1].ID != "google-2" {
		t.Fatalf("group order = %v", ids(groups))
	}
	g1, g2 := groups[0], groups[1]
	if g1.Slot != 1 || g1.Email != "a@x" || g1.Title != "Google AI Ultra" {
		t.Fatalf("primary group = %+v", g1)
	}
	if g2.Slot != 2 || g2.Email != "b@x" || g2.AccountName != "B" || g2.Title != "Google AI Pro" || g2.Detail != "Pro" {
		t.Fatalf("second group = %+v", g2)
	}

	want1 := []string{"antigravity/gemini_models", "colab/compute_units"}
	if got := itemIDs(g1); strings.Join(got, ",") != strings.Join(want1, ",") {
		t.Fatalf("primary items = %v", got)
	}
	if got := itemIDs(g2); len(got) != 1 || got[0] != "antigravity/gemini_models@2" {
		t.Fatalf("second account items = %v", got)
	}
}

func itemIDs(g engine.LayoutGroup) []string {
	out := make([]string, 0, len(g.Items))
	for _, it := range g.Items {
		out = append(out, it.ID)
	}
	return out
}

func ids(groups []engine.LayoutGroup) []string {
	out := make([]string, 0, len(groups))
	for _, g := range groups {
		out = append(out, g.ID)
	}
	return out
}
