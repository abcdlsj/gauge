package app

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abcdlsj/gauge/internal/store"
)

func newPlaceStore(t *testing.T) *store.Store {
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
	return st
}

// TestPlaceSignIn covers where a sign-in lands: the account the IDE is on
// becomes slot 1's fallback, a connected account keeps its slot, anything else
// takes the slot asked for — or a free one when slot 1 would tie it to the IDE's
// current account.
func TestPlaceSignIn(t *testing.T) {
	ctx := context.Background()
	st := newPlaceStore(t)
	_ = st.Set(ctx, "antigravity_ide_account", `{"email":"ide@x"}`)
	_ = st.Set(ctx, "antigravity_refresh_token_2", "rt2")
	_ = st.Set(ctx, "antigravity_account_2", `{"email":"two@x"}`)

	cases := []struct {
		asked    int
		email    string
		want     int
		mentions string
	}{
		{2, "IDE@x", 1, "the Antigravity IDE uses"},
		{3, "two@x", 2, "already connected as account 2"},
		{2, "two@x", 2, "signed in again"},
		{3, "new@x", 3, "Connected new@x"},
		{1, "other@x", 3, "the sign-in stays with other@x"},
		{1, "", 1, "Account connected"},
	}
	for _, tc := range cases {
		slot, note := placeSignIn(ctx, st, tc.asked, tc.email)
		if slot != tc.want || !strings.Contains(note, tc.mentions) {
			t.Fatalf("asked %d as %q: slot %d, %q; want %d mentioning %q", tc.asked, tc.email, slot, note, tc.want, tc.mentions)
		}
	}

	// An account whose token was removed no longer claims its slot.
	_ = st.Delete(ctx, "antigravity_refresh_token_2")
	if slot, _ := placeSignIn(ctx, st, 3, "two@x"); slot != 3 {
		t.Fatalf("slot = %d, want the slot asked for", slot)
	}
}
