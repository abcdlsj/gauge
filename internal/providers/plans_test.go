package providers

import "testing"

func TestParseCommandCode(t *testing.T) {
	body := []byte(`{"credits":{"monthlyCredits":120,"purchasedCredits":5},
	 "windowLimits":{"fiveHour":{"used":10,"cap":40,"resetAt":1763452800000},
	                 "weekly":{"used":90,"cap":100,"resetAt":0}}}`)
	snaps, err := parseCommandCode(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 2 {
		t.Fatalf("snaps = %d", len(snaps))
	}
	plan := snaps[0]
	if len(plan.Series) != 2 || plan.Series[0].Remaining != 75 || plan.Series[1].Remaining != 10 {
		t.Fatalf("series = %+v", plan.Series)
	}
	if plan.Series[0].ResetAt == nil || plan.Series[1].ResetAt != nil {
		t.Fatalf("reset handling wrong: %+v", plan.Series)
	}
	if plan.Remaining != 10 {
		t.Fatalf("headline = %v, want the tightest window", plan.Remaining)
	}
	if snaps[1].Remaining != 125 {
		t.Fatalf("credits = %v", snaps[1].Remaining)
	}
}

func TestParseCommandCodePayAsYouGo(t *testing.T) {
	if _, err := parseCommandCode([]byte(`{"windowLimits":{}}`)); err == nil {
		t.Fatal("want an error without windows or credits")
	}
}

func TestParseOpenDesign(t *testing.T) {
	body := []byte(`{"balanceUsd":0.993,"codingPlan":{"windows":[
	  {"durationSeconds":18000,"usedCredits":0,"limitCredits":100,"resetsAt":"2026-11-18T07:46:41Z"},
	  {"durationSeconds":604800,"usedCredits":16.23,"limitCredits":100,"resetsAt":"2026-11-19T06:46:41Z"},
	  {"durationSeconds":60,"usedCredits":1,"limitCredits":0}]}}`)
	snaps, err := parseOpenDesign(body)
	if err != nil {
		t.Fatal(err)
	}
	plan := snaps[0]
	if len(plan.Series) != 2 || plan.Series[0].Label != "5-hour" || plan.Series[1].Label != "Weekly" {
		t.Fatalf("series = %+v", plan.Series)
	}
	if plan.Series[1].ResetAt == nil {
		t.Fatal("reset not parsed")
	}
	if snaps[1].Unit != "USD" || snaps[1].Remaining != 0.993 {
		t.Fatalf("balance = %+v", snaps[1])
	}
}
