package visualization

import (
	"bytes"
	"compress/gzip"
	"strings"
	"testing"
	"time"

	"teal-heatmap/internal/process"
)

func TestRenderHeatmapSVGAndPageSizeFor3G(t *testing.T) {
	now := time.Date(2026, time.November, 22, 12, 0, 0, 0, time.UTC)
	counts := map[string]int{
		"2026-11-22": 12,
		"2026-11-21": 4,
		"2026-05-10": 25,
	}
	hm := process.BuildHeatmap(counts, 0, []int{2026}, now, time.UTC)
	svg := RenderHeatmapSVG(hm)

	if !strings.Contains(string(svg), `class="hm-svg"`) {
		t.Fatalf("expected hm-svg class in SVG output")
	}
	if !strings.Contains(string(svg), `data-d="2026-11-22"`) {
		t.Fatalf("expected 2026-11-22 cell in SVG output")
	}

	renderer, err := NewRenderer()
	if err != nil {
		t.Fatalf("NewRenderer failed: %v", err)
	}

	htmlBytes, err := renderer.RenderToBytes(PageData{
		Title:      "@alice.bsky.social",
		User:       &UserView{DID: "did:plc:test123", Handle: "alice.bsky.social"},
		LastSynced: "Nov 22, 2026 12:00 UTC",
		Heatmap:    hm,
		HeatmapSVG: svg,
	})
	if err != nil {
		t.Fatalf("RenderToBytes failed: %v", err)
	}

	// Verify gzipped first-load payload fits comfortably under 14 KB (initial TCP CWND for 3G).
	var gzBuf bytes.Buffer
	gz := gzip.NewWriter(&gzBuf)
	if _, err := gz.Write(htmlBytes); err != nil {
		t.Fatalf("gzip write failed: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip close failed: %v", err)
	}

	if gzBuf.Len() > 14*1024 {
		t.Fatalf("gzipped dashboard HTML is %d bytes (> 14 KB 3G budget)", gzBuf.Len())
	}
	t.Logf("Dashboard HTML size: raw=%d bytes, gzipped=%d bytes", len(htmlBytes), gzBuf.Len())
}
