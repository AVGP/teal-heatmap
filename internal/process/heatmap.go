package process

import (
	"fmt"
	"math"
	"sort"
	"time"
)

// PlayRecord represents a normalized teal.fm scrobble record.
type PlayRecord struct {
	DID         string    `json:"did"`
	Collection  string    `json:"collection"`
	RKey        string    `json:"rkey"`
	TrackName   string    `json:"trackName"`
	ArtistName  string    `json:"artistName"`
	ReleaseName string    `json:"releaseName,omitempty"`
	PlayedAt    time.Time `json:"playedAt"`
	OriginURL   string    `json:"originUrl,omitempty"`
}

// DailyCount represents the aggregated play count for a single calendar date (YYYY-MM-DD).
type DailyCount struct {
	Date  string // YYYY-MM-DD
	Count int
}

// HeatmapCell represents one square in the 53-week x 7-day GitHub-style calendar grid.
type HeatmapCell struct {
	Date    string // YYYY-MM-DD
	Count   int
	Level   int  // 0..4 intensity bucket
	InRange bool // false for leading/trailing padding cells outside the selected range
	WeekIdx int  // 0..52
	Weekday int  // 0 (Sunday) .. 6 (Saturday)
	Label   string
}

// MonthLabel marks the week column where a month starts for the top axis.
type MonthLabel struct {
	Name    string
	WeekIdx int
}

// SummaryStats holds high-level listening statistics for the selected period.
type SummaryStats struct {
	TotalPlays       int
	ActiveDays       int
	CurrentStreak    int
	LongestStreak    int
	MaxDayCount      int
	MaxDayDate       string
	AverageActiveDay float64
}

// HeatmapData is the complete model needed to render the SVG heatmap and stats cards.
type HeatmapData struct {
	YearLabel      string // e.g., "Last 12 months" or "2026"
	SelectedYear   int    // 0 means rolling last 365 days
	AvailableYears []int
	StartDate      time.Time
	EndDate        time.Time
	Weeks          int
	Cells          []HeatmapCell
	Months         []MonthLabel
	Thresholds     [4]int // upper bounds for levels 1..4
	Stats          SummaryStats
}

// BuildHeatmap constructs a GitHub-style Sunday-to-Saturday heatmap grid for either:
//   - year == 0: the rolling last 365 days ending on `now` (in `loc`)
//   - year > 0: the full calendar year Jan 1 .. Dec 31 of `year`
func BuildHeatmap(counts map[string]int, selectedYear int, availableYears []int, now time.Time, loc *time.Location) HeatmapData {
	if loc == nil {
		loc = time.UTC
	}
	nowLocal := now.In(loc)
	today := time.Date(nowLocal.Year(), nowLocal.Month(), nowLocal.Day(), 0, 0, 0, 0, loc)

	var rangeStart, rangeEnd time.Time
	var yearLabel string
	if selectedYear > 0 {
		rangeStart = time.Date(selectedYear, time.January, 1, 0, 0, 0, 0, loc)
		rangeEnd = time.Date(selectedYear, time.December, 31, 0, 0, 0, 0, loc)
		yearLabel = fmt.Sprintf("%d", selectedYear)
	} else {
		rangeEnd = today
		rangeStart = today.AddDate(0, 0, -364)
		yearLabel = "Last 12 months"
	}

	// Align grid start back to Sunday and grid end forward to Saturday.
	gridStart := rangeStart.AddDate(0, 0, -int(rangeStart.Weekday()))
	gridEnd := rangeEnd.AddDate(0, 0, 6-int(rangeEnd.Weekday()))

	// Compute positive counts inside the selected range to determine adaptive thresholds.
	var nonZero []int
	var stats SummaryStats
	for d := rangeStart; !d.After(rangeEnd); d = d.AddDate(0, 0, 1) {
		key := d.Format("2006-01-02")
		c := counts[key]
		if c > 0 {
			nonZero = append(nonZero, c)
			stats.TotalPlays += c
			stats.ActiveDays++
			if c > stats.MaxDayCount {
				stats.MaxDayCount = c
				stats.MaxDayDate = key
			}
		}
	}

	if stats.ActiveDays > 0 {
		stats.AverageActiveDay = float64(stats.TotalPlays) / float64(stats.ActiveDays)
	}

	thresholds := computeThresholds(nonZero)

	// Calculate longest streak within the selected range.
	run := 0
	for d := rangeStart; !d.After(rangeEnd); d = d.AddDate(0, 0, 1) {
		if counts[d.Format("2006-01-02")] > 0 {
			run++
			if run > stats.LongestStreak {
				stats.LongestStreak = run
			}
		} else {
			run = 0
		}
	}

	// Calculate current streak ending at today (or yesterday if nothing played today yet).
	streakAnchor := today
	if selectedYear > 0 && selectedYear < today.Year() {
		streakAnchor = rangeEnd
	}
	if counts[streakAnchor.Format("2006-01-02")] == 0 {
		yesterday := streakAnchor.AddDate(0, 0, -1)
		if !yesterday.Before(rangeStart) && counts[yesterday.Format("2006-01-02")] > 0 {
			streakAnchor = yesterday
		}
	}
	for d := streakAnchor; !d.Before(rangeStart); d = d.AddDate(0, 0, -1) {
		if counts[d.Format("2006-01-02")] > 0 {
			stats.CurrentStreak++
		} else {
			break
		}
	}

	// Build cells and month axis labels.
	totalDays := int(gridEnd.Sub(gridStart).Hours()/24) + 1
	weeks := (totalDays + 6) / 7
	cells := make([]HeatmapCell, 0, weeks*7)
	var months []MonthLabel
	lastMonth := time.Month(0)
	lastMonthWeek := -3

	for i := 0; i < weeks*7; i++ {
		d := gridStart.AddDate(0, 0, i)
		weekIdx := i / 7
		weekday := int(d.Weekday())
		inRange := !d.Before(rangeStart) && !d.After(rangeEnd)

		dateStr := d.Format("2006-01-02")
		c := 0
		level := 0
		if inRange {
			c = counts[dateStr]
			level = classifyLevel(c, thresholds)
		}

		// Place month label when a new month appears in the first week that contains its 1st day
		// (or the very first week of the range). Avoid overlapping adjacent labels.
		if inRange && d.Month() != lastMonth && (d.Day() <= 7 || i < 7) {
			if weekIdx-lastMonthWeek >= 2 {
				months = append(months, MonthLabel{
					Name:    d.Format("Jan"),
					WeekIdx: weekIdx,
				})
				lastMonthWeek = weekIdx
			}
			lastMonth = d.Month()
		}

		var label string
		switch c {
		case 0:
			label = fmt.Sprintf("No plays on %s", d.Format("Mon, Jan 2, 2006"))
		case 1:
			label = fmt.Sprintf("1 play on %s", d.Format("Mon, Jan 2, 2006"))
		default:
			label = fmt.Sprintf("%d plays on %s", c, d.Format("Mon, Jan 2, 2006"))
		}

		cells = append(cells, HeatmapCell{
			Date:    dateStr,
			Count:   c,
			Level:   level,
			InRange: inRange,
			WeekIdx: weekIdx,
			Weekday: weekday,
			Label:   label,
		})
	}

	// Ensure available years are sorted descending.
	yearsCopy := append([]int(nil), availableYears...)
	sort.Sort(sort.Reverse(sort.IntSlice(yearsCopy)))

	return HeatmapData{
		YearLabel:      yearLabel,
		SelectedYear:   selectedYear,
		AvailableYears: yearsCopy,
		StartDate:      rangeStart,
		EndDate:        rangeEnd,
		Weeks:          weeks,
		Cells:          cells,
		Months:         months,
		Thresholds:     thresholds,
		Stats:          stats,
	}
}

// computeThresholds computes quartile-based intensity thresholds (levels 1..4)
// so both casual listeners (3 plays/day) and heavy listeners (80 plays/day) get
// a well-balanced teal gradient.
func computeThresholds(nonZero []int) [4]int {
	if len(nonZero) == 0 {
		return [4]int{1, 3, 6, 10}
	}
	sorted := append([]int(nil), nonZero...)
	sort.Ints(sorted)

	q := func(p float64) int {
		idx := int(math.Ceil(p*float64(len(sorted)))) - 1
		if idx < 0 {
			idx = 0
		}
		if idx >= len(sorted) {
			idx = len(sorted) - 1
		}
		return sorted[idx]
	}

	t1 := max(1, q(0.25))
	t2 := max(t1+1, q(0.50))
	t3 := max(t2+1, q(0.75))
	t4 := max(t3+1, sorted[len(sorted)-1])
	return [4]int{t1, t2, t3, t4}
}

func classifyLevel(count int, thresholds [4]int) int {
	if count <= 0 {
		return 0
	}
	if count <= thresholds[0] {
		return 1
	}
	if count <= thresholds[1] {
		return 2
	}
	if count <= thresholds[2] {
		return 3
	}
	return 4
}
