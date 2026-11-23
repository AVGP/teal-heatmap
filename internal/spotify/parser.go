package spotify

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"teal-heatmap/internal/process"
)

const (
	// Collection is the synthetic collection name stored for imported Spotify plays.
	Collection = "spotify.export.play"

	// MinPlayedMS is the minimum play duration (30 seconds) required for a stream
	// to count as a play, matching standard scrobbling / teal.fm import behavior.
	MinPlayedMS = 30_000

	// maxUncompressedBytes guards against zip bombs while comfortably allowing
	// multi-decade Extended Streaming History exports (typically 50-200 MB raw).
	maxUncompressedBytes int64 = 512 << 20

	defaultBatchSize = 1000
)

// ImportSummary reports statistics from parsing a Spotify export ZIP archive.
type ImportSummary struct {
	ImportedPlays  int
	SkippedEntries int
	FilesProcessed int
}

// rawEntry covers both Spotify Extended Streaming History records
// (ts, ms_played, master_metadata_*) and standard Account Data records
// (endTime, msPlayed, artistName, trackName).
type rawEntry struct {
	// Extended Streaming History fields
	TS             string  `json:"ts"`
	MSPlayed       *int64  `json:"ms_played"`
	TrackName      *string `json:"master_metadata_track_name"`
	ArtistName     *string `json:"master_metadata_album_artist_name"`
	AlbumName      *string `json:"master_metadata_album_album_name"`
	TrackURI       *string `json:"spotify_track_uri"`

	// Standard Account Data StreamingHistory fields
	EndTime        string `json:"endTime"`
	LegacyArtist   string `json:"artistName"`
	LegacyTrack    string `json:"trackName"`
	LegacyMSPlayed *int64 `json:"msPlayed"`
}

// ParseZip streams all Spotify streaming history JSON files from a ZIP archive,
// converts valid music plays (>= 30s) into normalized process.PlayRecord entries,
// and delivers them in batches to onBatch.
func ParseZip(
	r io.ReaderAt,
	size int64,
	did string,
	onBatch func([]process.PlayRecord) error,
) (ImportSummary, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return ImportSummary{}, fmt.Errorf("open zip archive: %w", err)
	}

	var summary ImportSummary
	var totalUncompressed int64
	var seq int64
	batch := make([]process.PlayRecord, 0, defaultBatchSize)

	flush := func() error {
		if len(batch) == 0 || onBatch == nil {
			batch = batch[:0]
			return nil
		}
		if err := onBatch(batch); err != nil {
			return err
		}
		batch = batch[:0]
		return nil
	}

	for _, f := range zr.File {
		if f.FileInfo().IsDir() || !isStreamingHistoryJSON(f.Name) {
			continue
		}

		rc, err := f.Open()
		if err != nil {
			return summary, fmt.Errorf("open %s: %w", f.Name, err)
		}

		remaining := maxUncompressedBytes - totalUncompressed
		if remaining <= 0 {
			rc.Close()
			return summary, errors.New("zip archive exceeds maximum allowed uncompressed size")
		}

		lr := &io.LimitedReader{R: rc, N: remaining + 1}
		fileImported, fileSkipped, parseErr := parseJSONArray(lr, did, &seq, func(rec process.PlayRecord) error {
			batch = append(batch, rec)
			if len(batch) >= defaultBatchSize {
				return flush()
			}
			return nil
		})
		rc.Close()

		readBytes := (remaining + 1) - lr.N
		totalUncompressed += readBytes
		if totalUncompressed > maxUncompressedBytes {
			return summary, errors.New("zip archive exceeds maximum allowed uncompressed size")
		}
		if parseErr != nil {
			return summary, fmt.Errorf("parse %s: %w", f.Name, parseErr)
		}

		summary.FilesProcessed++
		summary.ImportedPlays += fileImported
		summary.SkippedEntries += fileSkipped
	}

	if err := flush(); err != nil {
		return summary, err
	}

	if summary.FilesProcessed == 0 {
		return summary, errors.New("no Spotify streaming history JSON files found in archive")
	}
	return summary, nil
}

// isStreamingHistoryJSON identifies Spotify audio/music streaming history JSON files
// while ignoring macOS metadata, PDFs, and video-only history files.
func isStreamingHistoryJSON(name string) bool {
	if strings.HasPrefix(name, "__MACOSX/") {
		return false
	}
	base := path.Base(name)
	if strings.HasPrefix(base, ".") {
		return false
	}
	lower := strings.ToLower(base)
	if !strings.HasSuffix(lower, ".json") {
		return false
	}
	// Extended Streaming History audio files: Streaming_History_Audio_*.json
	if strings.HasPrefix(lower, "streaming_history_audio") {
		return true
	}
	// Standard Account Data files: StreamingHistory_music_*.json or StreamingHistory0.json
	if strings.HasPrefix(lower, "streaminghistory_music") {
		return true
	}
	if strings.HasPrefix(lower, "streaminghistory") && !strings.Contains(lower, "podcast") && !strings.Contains(lower, "video") {
		return true
	}
	return false
}

func parseJSONArray(
	r io.Reader,
	did string,
	seq *int64,
	onRecord func(process.PlayRecord) error,
) (imported int, skipped int, err error) {
	dec := json.NewDecoder(r)

	tok, err := dec.Token()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return 0, 0, nil
		}
		return 0, 0, err
	}
	delim, ok := tok.(json.Delim)
	if !ok || delim != '[' {
		return 0, 0, fmt.Errorf("expected JSON array, got %v", tok)
	}

	for dec.More() {
		var raw rawEntry
		if err := dec.Decode(&raw); err != nil {
			return imported, skipped, err
		}
		rec, ok := convertEntry(did, *seq+1, raw)
		if !ok {
			skipped++
			continue
		}
		*seq++
		imported++
		if err := onRecord(rec); err != nil {
			return imported, skipped, err
		}
	}

	// Consume closing ']'
	if _, err := dec.Token(); err != nil {
		return imported, skipped, err
	}
	return imported, skipped, nil
}

func convertEntry(did string, seq int64, raw rawEntry) (process.PlayRecord, bool) {
	var (
		trackName  string
		artistName string
		albumName  string
		originURL  string
		msPlayed   int64
		playedAt   time.Time
	)

	if raw.TS != "" {
		// Extended Streaming History entry
		if raw.TrackName == nil {
			// Podcast episode, audiobook, or empty record.
			return process.PlayRecord{}, false
		}
		trackName = strings.TrimSpace(*raw.TrackName)
		if trackName == "" {
			return process.PlayRecord{}, false
		}
		if raw.MSPlayed != nil {
			msPlayed = *raw.MSPlayed
		}
		if msPlayed < MinPlayedMS {
			return process.PlayRecord{}, false
		}
		playedAt = parseSpotifyTimestamp(raw.TS)
		if playedAt.IsZero() {
			return process.PlayRecord{}, false
		}
		if raw.ArtistName != nil {
			artistName = strings.TrimSpace(*raw.ArtistName)
		}
		if raw.AlbumName != nil {
			albumName = strings.TrimSpace(*raw.AlbumName)
		}
		if raw.TrackURI != nil {
			originURL = trackURIToURL(*raw.TrackURI)
		}
	} else if raw.EndTime != "" {
		// Standard Account Data entry
		trackName = strings.TrimSpace(raw.LegacyTrack)
		if trackName == "" {
			return process.PlayRecord{}, false
		}
		if raw.LegacyMSPlayed != nil {
			msPlayed = *raw.LegacyMSPlayed
		}
		if msPlayed < MinPlayedMS {
			return process.PlayRecord{}, false
		}
		playedAt = parseSpotifyTimestamp(raw.EndTime)
		if playedAt.IsZero() {
			return process.PlayRecord{}, false
		}
		artistName = strings.TrimSpace(raw.LegacyArtist)
	} else {
		return process.PlayRecord{}, false
	}

	if artistName == "" {
		artistName = "Unknown Artist"
	}

	return process.PlayRecord{
		DID:         did,
		Collection:  Collection,
		RKey:        fmt.Sprintf("sp-%09d", seq),
		TrackName:   trackName,
		ArtistName:  artistName,
		ReleaseName: albumName,
		PlayedAt:    playedAt.UTC(),
		OriginURL:   originURL,
	}, true
}

func parseSpotifyTimestamp(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	formats := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05",
		"2006-01-02 15:04",
	}
	for _, f := range formats {
		if t, err := time.Parse(f, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

func trackURIToURL(uri string) string {
	uri = strings.TrimSpace(uri)
	if id, ok := strings.CutPrefix(uri, "spotify:track:"); ok && id != "" {
		return "https://open.spotify.com/track/" + id
	}
	return uri
}
