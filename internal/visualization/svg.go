package visualization

import (
	"fmt"
	"html"
	"html/template"
	"strings"

	"teal-heatmap/internal/process"
)

const (
	cellSize = 12
	cellGap  = 3
	cellStep = cellSize + cellGap
	leftPad  = 32
	topPad   = 20
	bottomPd = 6
)

// RenderHeatmapSVG builds an inline, accessible, compact SVG for the listening heatmap.
// Colors are driven by CSS custom properties (--hm-l0 .. --hm-l4) so both dark and light
// teal.fm themes apply seamlessly without regenerating the SVG.
func RenderHeatmapSVG(data process.HeatmapData) template.HTML {
	width := leftPad + data.Weeks*cellStep + 4
	height := topPad + 7*cellStep + bottomPd

	var b strings.Builder
	b.Grow(16 * 1024)

	fmt.Fprintf(&b,
		`<svg class="hm-svg" viewBox="0 0 %d %d" width="%d" height="%d" role="img" aria-label="Listening history heatmap for %s: %d total plays across %d active days">`,
		width, height, width, height,
		html.EscapeString(data.YearLabel),
		data.Stats.TotalPlays,
		data.Stats.ActiveDays,
	)

	// Month labels along top axis.
	b.WriteString(`<g class="hm-months" aria-hidden="true">`)
	for _, m := range data.Months {
		x := leftPad + m.WeekIdx*cellStep
		fmt.Fprintf(&b, `<text x="%d" y="12">%s</text>`, x, html.EscapeString(m.Name))
	}
	b.WriteString(`</g>`)

	// Day-of-week labels along left axis (Mon, Wed, Fri — matching GitHub's clean density).
	b.WriteString(`<g class="hm-days" aria-hidden="true">`)
	dayLabels := [3]struct {
		row  int
		text string
	}{
		{1, "Mon"},
		{3, "Wed"},
		{5, "Fri"},
	}
	for _, dl := range dayLabels {
		y := topPad + dl.row*cellStep + cellSize - 2
		fmt.Fprintf(&b, `<text x="0" y="%d">%s</text>`, y, dl.text)
	}
	b.WriteString(`</g>`)

	// Heatmap day cells.
	b.WriteString(`<g class="hm-grid">`)
	for _, c := range data.Cells {
		if !c.InRange {
			continue
		}
		x := leftPad + c.WeekIdx*cellStep
		y := topPad + c.Weekday*cellStep
		fmt.Fprintf(&b,
			`<rect class="hm-c l%d" x="%d" y="%d" width="%d" height="%d" rx="2.5" ry="2.5" data-d="%s" data-c="%d" tabindex="0" role="button" aria-label="%s"/>`,
			c.Level, x, y, cellSize, cellSize,
			c.Date, c.Count, html.EscapeString(c.Label),
		)
	}
	b.WriteString(`</g></svg>`)

	return template.HTML(b.String())
}
