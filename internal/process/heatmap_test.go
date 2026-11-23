package process

import (
	"testing"
	"time"
)

func TestBuildHeatmap_RollingYear(t *testing.T) {
	now := time.Date(2026, time.November, 22, 12, 0, 0, 0, time.UTC)
	counts := map[string]int{
		"2026-11-22": 10,
		"2026-11-21": 5,
		"2026-11-20": 2,
		"2026-11-18": 15,
		"2026-11-17": 8,
		"2026-11-16": 4,
		"2026-11-15": 1,
	}

	hm := BuildHeatmap(counts, 0, []int{2025, 2026}, now, time.UTC)

	if hm.YearLabel != "Last 12 months" {
		t.Fatalf("expected YearLabel 'Last 12 months', got %q", hm.YearLabel)
	}
	if hm.Stats.TotalPlays != 45 {
		t.Fatalf("expected 45 total plays, got %d", hm.Stats.TotalPlays)
	}
	if hm.Stats.ActiveDays != 7 {
		t.Fatalf("expected 7 active days, got %d", hm.Stats.ActiveDays)
	}
	if hm.Stats.CurrentStreak != 3 {
		t.Fatalf("expected current streak 3, got %d", hm.Stats.CurrentStreak)
	}
	if hm.Stats.LongestStreak != 4 {
		t.Fatalf("expected longest streak 4, got %d", hm.Stats.LongestStreak)
	}
	if hm.Stats.MaxDayDate != "2026-11-18" || hm.Stats.MaxDayCount != 15 {
		t.Fatalf("expected max day 2026-11-18 (15), got %s (%d)", hm.Stats.MaxDayDate, hm.Stats.MaxDayCount)
	}
	if len(hm.AvailableYears) != 2 || hm.AvailableYears[0] != 2026 {
		t.Fatalf("expected available years sorted desc, got %v", hm.AvailableYears)
	}
	if hm.Weeks < 52 || hm.Weeks > 54 {
		t.Fatalf("unexpected weeks count: %d", hm.Weeks)
	}
}

func TestBuildHeatmap_SpecificYear(t *testing.T) {
	now := time.Date(2026, time.November, 22, 12, 0, 0, 0, time.UTC)
	counts := map[string]int{
		"2025-01-01": 3,
		"2025-12-31": 7,
		"2026-01-01": 99, // outside 2025
	}

	hm := BuildHeatmap(counts, 2025, []int{2025, 2026}, now, time.UTC)
	if hm.YearLabel != "2025" {
		t.Fatalf("expected YearLabel '2025', got %q", hm.YearLabel)
	}
	if hm.Stats.TotalPlays != 10 {
		t.Fatalf("expected 10 total plays in 2025, got %d", hm.Stats.TotalPlays)
	}
	if hm.Stats.ActiveDays != 2 {
		t.Fatalf("expected 2 active days in 2025, got %d", hm.Stats.ActiveDays)
	}
}
