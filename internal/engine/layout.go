package engine

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/abcdlsj/gauge/internal/model"
)

// The dashboard is organised by subscription, not by collector: a Google AI
// subscription covers the Antigravity model pools, Flow credits and Colab
// compute units, so they live in one group. A group is level one, its cards
// (one per snapshot) level two; the user orders and hides both.

// LayoutKey stores the user's order and visibility as JSON (StoredLayout).
const LayoutKey = "display_layout"

// legacyHiddenKey is the flat card list used before groups existed.
const legacyHiddenKey = "display_hidden_cards"

// groupDef is a built-in group: which collectors feed it, in default order.
type groupDef struct {
	ID        string
	Title     string
	Providers []string
}

var groupDefs = []groupDef{
	{ID: "google", Title: "Google AI", Providers: []string{"antigravity", "google-flow", "colab"}},
	{ID: "commandcode", Title: "Command Code", Providers: []string{"commandcode"}},
	{ID: "opendesign", Title: "Open Design", Providers: []string{"opendesign"}},
	{ID: "gcp", Title: "Google Cloud", Providers: []string{"gcp-monitoring", "gcp-billing"}},
}

// metricOrder puts a collector's cards in a sensible default order; cards not
// listed follow in snapshot order.
var metricOrder = []string{"gemini_models", "claude_models", "other_models", "plan", "balance", "credits"}

// itemLabels name the cards that would be ambiguous once several collectors
// share a group.
var itemLabels = map[string]string{
	"antigravity/gemini_models": "Antigravity · Gemini",
	"antigravity/claude_models": "Antigravity · Claude & GPT",
	"antigravity/other_models":  "Antigravity · other models",
	"google-flow/flow_credits":  "Flow credits",
	"colab/compute_units":       "Colab compute units",
}

// defaultHidden applies until the user picks: the catch-all model pool is
// noise next to the Gemini and Claude pools.
var defaultHidden = []string{"antigravity/other_models"}

// LayoutItem is one card (level two).
type LayoutItem struct {
	ID       string `json:"id"` // provider/metric
	Label    string `json:"label"`
	Provider string `json:"provider"`
	Metric   string `json:"metric"`
	Hidden   bool   `json:"hidden"`
}

// LayoutGroup is one subscription (level one).
type LayoutGroup struct {
	ID        string       `json:"id"`
	Title     string       `json:"title"`
	Detail    string       `json:"detail,omitempty"` // plan, e.g. "Ultra 5x"
	Providers []string     `json:"providers"`
	Hidden    bool         `json:"hidden"`
	Items     []LayoutItem `json:"items"`
}

// StoredLayout is what the settings page writes: order plus hidden flags.
type StoredLayout struct {
	Groups []StoredGroup `json:"groups"`
}

type StoredGroup struct {
	ID     string       `json:"id"`
	Hidden bool         `json:"hidden"`
	Items  []StoredItem `json:"items"`
}

type StoredItem struct {
	ID     string `json:"id"`
	Hidden bool   `json:"hidden"`
}

// Validate drops malformed entries and caps the size, so a bad client cannot
// store junk.
func (l StoredLayout) Validate() StoredLayout {
	var out StoredLayout
	seenGroup := map[string]bool{}
	seenItem := map[string]bool{}
	for _, g := range l.Groups {
		if g.ID == "" || len(g.ID) > 64 || seenGroup[g.ID] || len(out.Groups) >= 50 {
			continue
		}
		seenGroup[g.ID] = true
		sg := StoredGroup{ID: g.ID, Hidden: g.Hidden}
		for _, it := range g.Items {
			p, m, ok := strings.Cut(it.ID, "/")
			if !ok || p == "" || m == "" || len(it.ID) > 200 || seenItem[it.ID] || len(sg.Items) >= 200 {
				continue
			}
			seenItem[it.ID] = true
			sg.Items = append(sg.Items, it)
		}
		out.Groups = append(out.Groups, sg)
	}
	return out
}

func (e *Engine) storedLayout(ctx context.Context) (StoredLayout, bool) {
	raw, ok, err := e.store.Get(ctx, LayoutKey)
	if err != nil || !ok {
		return StoredLayout{}, false
	}
	var l StoredLayout
	if json.Unmarshal([]byte(raw), &l) != nil {
		return StoredLayout{}, false
	}
	return l, true
}

// defaultHiddenSet is used until a layout is stored: the legacy flat list if
// one exists, else the built-in default.
func (e *Engine) defaultHiddenSet(ctx context.Context) map[string]bool {
	list := defaultHidden
	if raw, ok, err := e.store.Get(ctx, legacyHiddenKey); err == nil && ok {
		var legacy []string
		if json.Unmarshal([]byte(raw), &legacy) == nil {
			list = legacy
		}
	}
	out := map[string]bool{}
	for _, id := range list {
		out[id] = true
	}
	return out
}

// Layout resolves the stored preferences against the cards that exist now.
// Groups are always listed (the settings page needs them); a group with no
// cards simply has no items. New cards join the end of their group.
func (e *Engine) Layout(ctx context.Context, quotas []model.Snapshot, accounts []model.Account) []LayoutGroup {
	stored, haveStored := e.storedLayout(ctx)
	defaults := e.defaultHiddenSet(ctx)

	// The groups this build knows, plus one per unknown collector so a new
	// provider is never invisible.
	defs := slices.Clone(groupDefs)
	owner := map[string]string{}
	for _, d := range defs {
		for _, p := range d.Providers {
			owner[p] = d.ID
		}
	}
	for _, p := range e.providers {
		if _, ok := owner[p.Name()]; !ok {
			defs = append(defs, groupDef{ID: p.Name(), Title: p.Title(), Providers: []string{p.Name()}})
			owner[p.Name()] = p.Name()
		}
	}

	// Cards per group in default order.
	cards := map[string][]LayoutItem{}
	for _, d := range defs {
		var list []LayoutItem
		for _, prov := range d.Providers {
			var mine []model.Snapshot
			for _, q := range quotas {
				if q.Provider == prov && q.Kind != "note" {
					mine = append(mine, q)
				}
			}
			slices.SortStableFunc(mine, func(a, b model.Snapshot) int {
				return rank(a.Metric) - rank(b.Metric)
			})
			for _, q := range mine {
				id := q.Provider + "/" + q.Metric
				label := itemLabels[id]
				if label == "" {
					label = q.Label
				}
				list = append(list, LayoutItem{ID: id, Label: label, Provider: q.Provider, Metric: q.Metric, Hidden: !haveStored && defaults[id]})
			}
		}
		cards[d.ID] = list
	}

	// Group order: stored first, then the rest in default order.
	byID := map[string]groupDef{}
	for _, d := range defs {
		byID[d.ID] = d
	}
	var order []string
	storedGroup := map[string]StoredGroup{}
	for _, g := range stored.Groups {
		if _, ok := byID[g.ID]; ok && storedGroup[g.ID].ID == "" {
			order = append(order, g.ID)
			storedGroup[g.ID] = g
		}
	}
	for _, d := range defs {
		if _, ok := storedGroup[d.ID]; !ok {
			order = append(order, d.ID)
		}
	}

	out := make([]LayoutGroup, 0, len(order))
	for _, id := range order {
		d := byID[id]
		g := LayoutGroup{ID: d.ID, Title: d.Title, Providers: d.Providers, Items: []LayoutItem{}}
		if d.ID == "google" {
			g.Title, g.Detail = googleTitle(accounts)
		}
		sg, has := storedGroup[id]
		g.Hidden = has && sg.Hidden

		present := map[string]LayoutItem{}
		for _, it := range cards[id] {
			present[it.ID] = it
		}
		used := map[string]bool{}
		if has {
			for _, s := range sg.Items {
				it, ok := present[s.ID]
				if !ok || used[s.ID] {
					continue
				}
				it.Hidden = s.Hidden
				g.Items = append(g.Items, it)
				used[s.ID] = true
			}
		}
		for _, it := range cards[id] {
			if used[it.ID] {
				continue
			}
			// A card the stored layout has never seen follows the defaults.
			it.Hidden = defaults[it.ID]
			g.Items = append(g.Items, it)
		}
		out = append(out, g)
	}
	return out
}

func rank(metric string) int {
	if i := slices.Index(metricOrder, metric); i >= 0 {
		return i
	}
	return len(metricOrder)
}

// googleTitle names the Google group after the subscription of the primary
// account: "Gemini Ultra", "Gemini Pro". Detail keeps the exact plan.
func googleTitle(accounts []model.Account) (title, detail string) {
	if len(accounts) == 0 || accounts[0].Tier == "" {
		return "Google AI", ""
	}
	tier := accounts[0].Tier
	l := strings.ToLower(tier)
	switch {
	case strings.Contains(l, "ultra"):
		return "Gemini Ultra", tier
	case strings.Contains(l, "pro"):
		return "Gemini Pro", tier
	case strings.Contains(l, "free"):
		return "Gemini Free", tier
	}
	return "Google AI", tier
}
