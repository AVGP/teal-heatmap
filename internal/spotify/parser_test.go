package spotify

import (
	"archive/zip"
	"bytes"
	"testing"
	"time"

	"teal-heatmap/internal/process"
	"teal-heatmap/internal/visualization"
)

func buildTestZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("create zip entry %s: %v", name, err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatalf("write zip entry %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return buf.Bytes()
}

func TestParseZipExtendedHistory(t *testing.T) {
	zipBytes := buildTestZip(t, map[string]string{
		"Spotify Extended Streaming History/ReadMeFirst_ExtendedStreamingHistory.pdf": "pdf bytes",
		"Spotify Extended Streaming History/Streaming_History_Video_2025.json": `[
			{"ts":"2025-01-14T16:26:08Z","ms_played":60000,"master_metadata_track_name":"Video Track","master_metadata_album_artist_name":"Vid Artist"}
		]`,
		"Spotify Extended Streaming History/Streaming_History_Audio_2025.json": `[
			{
				"ts": "2025-05-01T10:00:00Z",
				"ms_played": 210000,
				"master_metadata_track_name": "Hey Boy Hey Girl",
				"master_metadata_album_artist_name": "The Chemical Brothers",
				"master_metadata_album_album_name": "Surrender",
				"spotify_track_uri": "spotify:track:7kXmJwrZGIhDaLT9sNo3ut"
			},
			{
				"ts": "2025-05-01T10:00:00Z",
				"ms_played": 180000,
				"master_metadata_track_name": "Block Rockin' Beats",
				"master_metadata_album_artist_name": "The Chemical Brothers",
				"master_metadata_album_album_name": "Dig Your Own Hole",
				"spotify_track_uri": "spotify:track:abc123"
			},
			{
				"ts": "2025-05-01T11:00:00Z",
				"ms_played": 12000,
				"master_metadata_track_name": "Skipped Track",
				"master_metadata_album_artist_name": "Someone"
			},
			{
				"ts": "2025-05-01T12:00:00Z",
				"ms_played": 90000,
				"master_metadata_track_name": null,
				"episode_name": "Some Podcast Episode",
				"episode_show_name": "Some Podcast"
			}
		]`,
	})

	var plays []process.PlayRecord
	summary, err := ParseZip(bytes.NewReader(zipBytes), int64(len(zipBytes)), "spotify:test", func(batch []process.PlayRecord) error {
		plays = append(plays, batch...)
		return nil
	})
	if err != nil {
		t.Fatalf("ParseZip failed: %v", err)
	}

	if summary.FilesProcessed != 1 {
		t.Fatalf("expected 1 file processed, got %d", summary.FilesProcessed)
	}
	if summary.ImportedPlays != 2 {
		t.Fatalf("expected 2 imported plays, got %d", summary.ImportedPlays)
	}
	if summary.SkippedEntries != 2 {
		t.Fatalf("expected 2 skipped entries, got %d", summary.SkippedEntries)
	}
	if len(plays) != 2 {
		t.Fatalf("expected 2 plays delivered, got %d", len(plays))
	}

	first := plays[0]
	if first.TrackName != "Hey Boy Hey Girl" || first.ArtistName != "The Chemical Brothers" || first.ReleaseName != "Surrender" {
		t.Errorf("unexpected first play metadata: %+v", first)
	}
	if first.OriginURL != "https://open.spotify.com/track/7kXmJwrZGIhDaLT9sNo3ut" {
		t.Errorf("unexpected origin URL: %q", first.OriginURL)
	}
	if first.RKey == plays[1].RKey {
		t.Errorf("expected distinct RKeys for duplicate-timestamp offline plays, both got %q", first.RKey)
	}
}

func TestParseZipStandardAccountData(t *testing.T) {
	zipBytes := buildTestZip(t, map[string]string{
		"MyData/StreamingHistory_music_0.json": `[
			{"endTime":"2025-03-10 14:22","artistName":"Daft Punk","trackName":"Aerodynamic","msPlayed":212000},
			{"endTime":"2025-03-10 14:23","artistName":"Daft Punk","trackName":"Too Short","msPlayed":5000}
		]`,
	})

	var plays []process.PlayRecord
	summary, err := ParseZip(bytes.NewReader(zipBytes), int64(len(zipBytes)), "spotify:legacy", func(batch []process.PlayRecord) error {
		plays = append(plays, batch...)
		return nil
	})
	if err != nil {
		t.Fatalf("ParseZip failed: %v", err)
	}
	if summary.ImportedPlays != 1 || len(plays) != 1 {
		t.Fatalf("expected 1 imported play, got summary=%+v len=%d", summary, len(plays))
	}
	wantTime := time.Date(2025, 3, 10, 14, 22, 0, 0, time.UTC)
	if !plays[0].PlayedAt.Equal(wantTime) {
		t.Errorf("expected PlayedAt=%v, got %v", wantTime, plays[0].PlayedAt)
	}
}

func TestParseZipNoHistoryFiles(t *testing.T) {
	zipBytes := buildTestZip(t, map[string]string{
		"README.txt": "nothing here",
	})
	_, err := ParseZip(bytes.NewReader(zipBytes), int64(len(zipBytes)), "spotify:empty", nil)
	if err == nil {
		t.Fatal("expected error when archive has no Spotify streaming history files")
	}
}

func TestSpotifyAndTealProduceIdenticalHeatmap(t *testing.T) {
	zipBytes := buildTestZip(t, map[string]string{
		"Spotify Extended Streaming History/Streaming_History_Audio_2025.json": `[
			{"ts":"2025-04-01T10:00:00Z","ms_played":180000,"master_metadata_track_name":"Track A","master_metadata_album_artist_name":"Artist A"},
			{"ts":"2025-04-01T12:00:00Z","ms_played":200000,"master_metadata_track_name":"Track B","master_metadata_album_artist_name":"Artist B"},
			{"ts":"2025-04-02T09:30:00Z","ms_played":240000,"master_metadata_track_name":"Track C","master_metadata_album_artist_name":"Artist A"}
		]`,
	})

	var spPlays []process.PlayRecord
	_, err := ParseZip(bytes.NewReader(zipBytes), int64(len(zipBytes)), "spotify:same", func(batch []process.PlayRecord) error {
		spPlays = append(spPlays, batch...)
		return nil
	})
	if err != nil {
		t.Fatalf("ParseZip failed: %v", err)
	}

	spCounts := map[string]int{}
	for _, p := range spPlays {
		spCounts[p.PlayedAt.UTC().Format("2006-01-02")]++
	}

	// Same data as teal.fm records.
	tealCounts := map[string]int{
		"2025-04-01": 2,
		"2025-04-02": 1,
	}

	now := time.Date(2025, 12, 31, 0, 0, 0, 0, time.UTC)
	spHM := process.BuildHeatmap(spCounts, 2025, []int{2025}, now, time.UTC)
	tealHM := process.BuildHeatmap(tealCounts, 2025, []int{2025}, now, time.UTC)

	spSVG := visualization.RenderHeatmapSVG(spHM)
	tealSVG := visualization.RenderHeatmapSVG(tealHM)
	if spSVG != tealSVG {
		t.Fatal("expected identical SVG heatmap output for identical Spotify and teal.fm data")
	}
}
