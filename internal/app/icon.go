package app

import (
	"context"
	"net/http"

	"github.com/abcdlsj/gauge/internal/store"
	"github.com/abcdlsj/gauge/web"
)

// IconSetting picks the mark shown in the menu bar, the popover header, the
// web pages and the favicon. The name avoids the words store.sensitive
// treats as secret, so the value stays in plain text.
const IconSetting = "app_icon"

// Icon choices. Their artwork comes from scripts/make_icons.py.
const (
	IconGauge   = "gauge"
	IconClassic = "classic"
)

// markFiles maps each icon choice to its embedded web mark.
var markFiles = map[string]string{
	IconGauge:   "mark.svg",
	IconClassic: "mark-classic.svg",
}

// Icon returns the stored icon choice, falling back to the default when the
// value is unset or unknown.
func (a *App) Icon(ctx context.Context) string {
	return storedIcon(ctx, a.Store)
}

func storedIcon(ctx context.Context, st *store.Store) string {
	raw, ok, err := st.Get(ctx, IconSetting)
	if err != nil || !ok {
		return IconGauge
	}
	return NormalizeIcon(raw)
}

// NormalizeIcon maps an empty or unknown value to the default icon.
func NormalizeIcon(v string) string {
	if _, ok := markFiles[v]; ok {
		return v
	}
	return IconGauge
}

// handleMark serves the web mark for the chosen icon. ?icon= overrides the
// setting, so the picker can show every choice from the same route.
func (s *Server) handleMark(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("icon")
	if name == "" {
		name = storedIcon(r.Context(), s.store)
	}
	raw, err := web.FS.ReadFile(markFiles[NormalizeIcon(name)])
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(raw)
}
