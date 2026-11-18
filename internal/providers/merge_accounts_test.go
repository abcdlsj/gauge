package providers

import (
	"testing"

	"github.com/abcdlsj/gauge/internal/model"
)

func TestMergeAccountsKeepsOnePerSlot(t *testing.T) {
	base := []model.Account{
		{Slot: 1, Email: "a@x", Tier: "Ultra 5x"},
		{Slot: 2, Email: "b@x"},
	}
	extra := []model.Account{
		{Slot: 2, Email: "b@x", Tier: "Pro"},
		{Slot: 3, Email: "c@x", Tier: "Free"},
	}
	got := mergeAccounts(base, extra)
	if len(got) != 3 {
		t.Fatalf("want 3 accounts, got %d: %+v", len(got), got)
	}
	if got[1].Slot != 2 || got[1].Tier != "Pro" {
		t.Fatalf("slot 2 lost its tier: %+v", got[1])
	}
	if got[0].Tier != "Ultra 5x" || got[2].Email != "c@x" {
		t.Fatalf("unexpected merge: %+v", got)
	}
}
