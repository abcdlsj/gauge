package engine

import (
	"context"
	"io"
	"log"
	"path/filepath"
	"testing"

	"github.com/abcdlsj/gauge/internal/model"
	"github.com/abcdlsj/gauge/internal/providers"
	"github.com/abcdlsj/gauge/internal/store"
)

func newLayoutEngine(t *testing.T) *Engine {
	t.Helper()
	dir := t.TempDir()
	key, err := store.LoadKey(filepath.Join(dir, "k"))
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(dir, "q.db"), key)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.InitSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	logger := log.New(io.Discard, "", 0)
	return New(st, providers.NewDeps(st, logger), nil, logger)
}

func groupOf(groups []LayoutGroup, id string) (LayoutGroup, bool) {
	for _, g := range groups {
		if g.ID == id {
			return g, true
		}
	}
	return LayoutGroup{}, false
}

func itemIDs(g LayoutGroup) map[string]bool {
	out := map[string]bool{}
	for _, it := range g.Items {
		out[it.ID] = true
	}
	return out
}

// TestFlowAndColabFollowTheirAccount covers the three places the browser
// session's cards can land: the IDE's group, an extra account's group, and a
// group of their own.
func TestFlowAndColabFollowTheirAccount(t *testing.T) {
	ctx := context.Background()
	accounts := []model.Account{
		{Slot: 1, Email: "ide@x", Primary: true},
		{Slot: 2, Email: "two@x"},
	}
	snaps := func(email string) []model.Snapshot {
		return []model.Snapshot{
			{Provider: "antigravity", Metric: "gemini_models"},
			{Provider: "antigravity", Metric: "gemini_models@2"},
			{Provider: "google-flow", Metric: "flow_credits", Account: email},
			{Provider: "colab", Metric: "compute_units", Account: email},
		}
	}

	cases := []struct {
		name, email, want string
	}{
		{"same as the IDE account", "IDE@x", "google"},
		{"same as account 2", "two@x", "google-2"},
		{"no Antigravity account", "else@x", SessionGroupID},
		{"account not known yet", "", "google"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newLayoutEngine(t)
			groups := e.Layout(ctx, snaps(tc.email), accounts)
			g, ok := groupOf(groups, tc.want)
			if !ok {
				t.Fatalf("no group %q in %+v", tc.want, groups)
			}
			ids := itemIDs(g)
			if !ids["google-flow/flow_credits"] || !ids["colab/compute_units"] {
				t.Fatalf("%s lacks Flow/Colab: %+v", tc.want, g.Items)
			}
			for _, other := range groups {
				if other.ID == tc.want {
					continue
				}
				if itemIDs(other)["google-flow/flow_credits"] {
					t.Fatalf("Flow also shown in %s", other.ID)
				}
			}
			// The Antigravity cards never move with them.
			if !itemIDs(mustGroup(t, groups, "google"))["antigravity/gemini_models"] ||
				!itemIDs(mustGroup(t, groups, "google-2"))["antigravity/gemini_models@2"] {
				t.Fatal("Antigravity cards moved")
			}
			_, alone := groupOf(groups, SessionGroupID)
			if alone != (tc.want == SessionGroupID) {
				t.Fatalf("session group present = %v", alone)
			}
		})
	}
}

// TestSessionGroupNamesItsAccount checks the separate group carries the
// cookie's account, so it is not read as one of the Antigravity accounts.
func TestSessionGroupNamesItsAccount(t *testing.T) {
	ctx := context.Background()
	e := newLayoutEngine(t)
	_ = e.store.Set(ctx, providers.SessionAccountKey("google-flow"), `{"email":"else@x","name":"Else","fp":"f"}`)
	groups := e.Layout(ctx, []model.Snapshot{
		{Provider: "google-flow", Metric: "flow_credits", Account: "else@x"},
	}, []model.Account{{Slot: 1, Email: "ide@x"}})
	g := mustGroup(t, groups, SessionGroupID)
	if g.Email != "else@x" || g.AccountName != "Else" {
		t.Fatalf("session group = %+v", g)
	}
}

func mustGroup(t *testing.T, groups []LayoutGroup, id string) LayoutGroup {
	t.Helper()
	g, ok := groupOf(groups, id)
	if !ok {
		t.Fatalf("no group %q", id)
	}
	return g
}
