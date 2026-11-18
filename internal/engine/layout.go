package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/abcdlsj/gauge/internal/model"
	"github.com/abcdlsj/gauge/internal/providers"
)

// The dashboard is organised by subscription, not by collector: a Google AI
// subscription covers the Antigravity model pools, Flow credits and Colab
// compute units, so they live in one group. A group is level one, its cards
// (one per snapshot) level two; the user orders and hides both.

// LayoutKey stores the user's order and visibility as JSON (StoredLayout).
const LayoutKey = "display_layout"

// groupDef is a built-in group: which collectors feed it, in default order.
// Slot is set when the group belongs to one signed-in identity (Google AI).
type groupDef struct {
	ID        string
	Title     string
	Providers []string
	Slot      int
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
	Account  string `json:"account,omitempty"` // identity that produced the card
}

// LayoutGroup is one subscription (level one). Each signed-in Google
// identity is its own group, so it can be ordered and hidden like any other
// subscription and shows its own avatar and plan.
type LayoutGroup struct {
	ID          string       `json:"id"`
	Title       string       `json:"title"`
	Detail      string       `json:"detail,omitempty"` // plan, e.g. "Ultra 5x"
	Providers   []string     `json:"providers"`
	Hidden      bool         `json:"hidden"`
	Items       []LayoutItem `json:"items"`
	Slot        int          `json:"slot,omitempty"`         // 0 = not tied to one identity
	Email       string       `json:"email,omitempty"`        // identity shown in the header
	AccountName string       `json:"account_name,omitempty"` // display name of that identity
	Picture     string       `json:"picture,omitempty"`
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
		sg.Items, _ = validItems(g.Items, seenItem)
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

// validItems drops malformed cards and duplicates, returning the kept list and
// the ids it added to seen.
func validItems(in []StoredItem, seen map[string]bool) ([]StoredItem, []string) {
	var out []StoredItem
	var added []string
	for _, it := range in {
		p, m, ok := strings.Cut(it.ID, "/")
		if !ok || p == "" || m == "" || len(it.ID) > 200 || seen[it.ID] || len(out) >= 200 {
			continue
		}
		seen[it.ID] = true
		out = append(out, it)
		added = append(added, it.ID)
	}
	return out, added
}

// defaultHiddenSet is used until a layout is stored.
func (e *Engine) defaultHiddenSet() map[string]bool {
	out := map[string]bool{}
	for _, id := range defaultHidden {
		out[id] = true
	}
	return out
}

// Layout resolves the stored preferences against the cards that exist now.
// Groups are always listed (the settings page needs them); a group with no
// cards simply has no items. New cards join the end of their group.
func (e *Engine) Layout(ctx context.Context, quotas []model.Snapshot, accounts []model.Account) []LayoutGroup {
	stored, haveStored := e.storedLayout(ctx)
	defaults := e.defaultHiddenSet()

	// The groups this build knows, plus one per unknown collector so a new
	// provider is never invisible.
	defs := googleGroupDefs(accounts)
	defs = append(defs, e.instanceGroupDefs(ctx, "commandcode", "Command Code", "commandcode_api_key", quotas)...)
	defs = append(defs, e.instanceGroupDefs(ctx, "opendesign", "Open Design", "opendesign_control_key", quotas)...)
	for _, d := range groupDefs {
		if d.ID == "gcp" {
			defs = append(defs, d)
		}
	}
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
				if q.Provider != prov || q.Kind == "note" {
					continue
				}
				// A per-identity group only owns the cards that identity
				// produced; the collector tags them with "@N".
				if d.Slot > 0 && accountSlot(q.Metric) != d.Slot {
					continue
				}
				mine = append(mine, q)
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
				list = append(list, LayoutItem{ID: id, Label: label, Provider: q.Provider, Metric: q.Metric, Account: q.Account, Hidden: !haveStored && defaults[id]})
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
	for i, d := range defs {
		if _, ok := storedGroup[d.ID]; ok {
			continue
		}
		// A new identity lands right after the previous identity group, not at
		// the bottom of the list. Everything else appends in default order.
		if d.Slot > 1 {
			at := len(order)
			for j := i - 1; j >= 0 && defs[j].Slot > 0; j-- {
				if pos := slices.Index(order, defs[j].ID); pos >= 0 {
					at = pos + 1
					break
				}
			}
			order = slices.Insert(order, at, d.ID)
			continue
		}
		order = append(order, d.ID)
	}

	out := make([]LayoutGroup, 0, len(order))
	for _, id := range order {
		d := byID[id]
		g := LayoutGroup{ID: d.ID, Title: d.Title, Providers: d.Providers, Items: []LayoutItem{}}
		sg, has := storedGroup[id]
		g.Hidden = has && sg.Hidden

		g.Items = orderedItems(cards[id], sg.Items, defaults)
		if d.Slot > 0 {
			if strings.HasPrefix(d.ID, "google") {
				if acc, ok := findAccount(accounts, d.Slot); ok {
					g.Slot = d.Slot
					g.Email = acc.Email
					g.AccountName = acc.Name
					g.Picture = acc.Picture
					g.Title, g.Detail = googleTitleFor(acc)
				}
			} else {
				// Instanced sources have no avatars: the account name the
				// collector reported, else the label the user typed when the
				// instance was added (it may still have no snapshots).
				g.Slot = d.Slot
				for _, it := range g.Items {
					if it.Account != "" {
						g.AccountName = it.Account
						break
					}
				}
				if g.AccountName == "" && d.Slot > 1 && len(d.Providers) > 0 {
					if v, ok, _ := e.store.Get(ctx, providers.InstanceNameKey(d.Providers[0], d.Slot)); ok {
						g.AccountName = v
					}
				}
				g.Detail = g.AccountName
				if d.Slot > 1 {
					g.Title = d.Title
					if g.AccountName != "" {
						g.Title = d.Title + " · " + g.AccountName
					}
				}
			}
		}
		out = append(out, g)
	}
	return out
}

// orderedItems applies the stored order and hidden flags to the cards that
// exist now; unseen cards join the end and follow the defaults.
func orderedItems(cards []LayoutItem, stored []StoredItem, defaults map[string]bool) []LayoutItem {
	present := map[string]LayoutItem{}
	for _, it := range cards {
		present[it.ID] = it
	}
	out := make([]LayoutItem, 0, len(cards))
	used := map[string]bool{}
	for _, s := range stored {
		it, ok := present[s.ID]
		if !ok || used[s.ID] {
			continue
		}
		it.Hidden = s.Hidden
		out = append(out, it)
		used[s.ID] = true
	}
	for _, it := range cards {
		if used[it.ID] {
			continue
		}
		it.Hidden = defaults[it.ID]
		out = append(out, it)
	}
	return out
}

// instanceGroupDefs splits a source into one group per configured instance.
// Slot 1 is always listed so an unconfigured source stays reachable from the
// settings page. Extra slots exist once they have a name or credential, or a
// snapshot that carries their @N suffix.
func (e *Engine) instanceGroupDefs(ctx context.Context, provider, title, baseKey string, quotas []model.Snapshot) []groupDef {
	slots := map[int]bool{1: true}
	for _, q := range quotas {
		if q.Provider == provider {
			slots[accountSlot(q.Metric)] = true
		}
	}
	for slot := 2; slot <= 3; slot++ {
		for _, key := range []string{
			providers.InstanceKey(baseKey, slot),
			providers.InstanceNameKey(provider, slot),
		} {
			if v, ok, _ := e.store.Get(ctx, key); ok && v != "" {
				slots[slot] = true
			}
		}
	}
	var out []groupDef
	for slot := 1; slot <= 3; slot++ {
		if !slots[slot] {
			continue
		}
		id := provider
		if slot > 1 {
			id = fmt.Sprintf("%s-%d", provider, slot)
		}
		out = append(out, groupDef{ID: id, Title: title, Providers: []string{provider}, Slot: slot})
	}
	return out
}

// googleGroupDefs splits Google AI into one group per signed-in identity.
// Slot 1 keeps the historic "google" id so stored layouts survive; extra
// identities get "google-N".
func googleGroupDefs(accounts []model.Account) []groupDef {
	defs := []groupDef{{
		ID:        "google",
		Title:     "Google AI",
		Providers: []string{"antigravity", "google-flow", "colab"},
		Slot:      1,
	}}
	for _, a := range accounts {
		if a.Slot <= 1 {
			continue
		}
		defs = append(defs, groupDef{
			ID:        fmt.Sprintf("google-%d", a.Slot),
			Title:     "Google AI",
			Providers: []string{"antigravity"},
			Slot:      a.Slot,
		})
	}
	slices.SortStableFunc(defs, func(a, b groupDef) int { return a.Slot - b.Slot })
	return defs
}

func findAccount(accounts []model.Account, slot int) (model.Account, bool) {
	for _, a := range accounts {
		if a.Slot == slot {
			return a, true
		}
	}
	return model.Account{}, false
}

// accountSlot reads the slot the Antigravity collector appended to a metric
// ("gemini_models@2"); every other card belongs to the primary identity.
func accountSlot(metric string) int {
	i := strings.LastIndex(metric, "@")
	if i < 0 {
		return 1
	}
	n, err := strconv.Atoi(metric[i+1:])
	if err != nil || n < 2 {
		return 1
	}
	return n
}

func rank(metric string) int {
	if i := slices.Index(metricOrder, metric); i >= 0 {
		return i
	}
	return len(metricOrder)
}

// googleTitleFor names the Google group after the subscription of one account:
// "Google AI Ultra", "Google AI Pro". Detail keeps the exact plan. Google
// renamed Google One AI Premium to Google AI Pro/Ultra in 2025, so the title
// follows Google's own product names rather than the Gemini model name.
func googleTitleFor(account model.Account) (title, detail string) {
	if account.Tier == "" {
		return "Google AI", ""
	}
	tier := account.Tier
	l := strings.ToLower(tier)
	switch {
	case strings.Contains(l, "ultra"):
		return "Google AI Ultra", tier
	case strings.Contains(l, "pro"):
		return "Google AI Pro", tier
	case strings.Contains(l, "free"):
		return "Google AI Free", tier
	}
	return "Google AI", tier
}
