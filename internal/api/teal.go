package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"teal-heatmap/internal/process"
)

// TealCollections lists the current and legacy teal.fm play record collections.
var TealCollections = []string{
	"fm.teal.feed.play",
	"fm.teal.alpha.feed.play",
}

// TealClient fetches teal.fm play records from an ATProto Personal Data Server (PDS).
type TealClient struct {
	HTTPClient *http.Client
	UserAgent  string
	MaxPages   int
}

// NewTealClient creates a TealClient with sensible HTTP timeouts.
// MaxPages defaults to 0 (unlimited) so full listening histories are fetched.
func NewTealClient() *TealClient {
	return &TealClient{
		HTTPClient: &http.Client{
			Timeout: 20 * time.Second,
		},
		UserAgent: "teal-heatmap/1.0",
		MaxPages:  0,
	}
}

type listRecordsResponse struct {
	Cursor  string            `json:"cursor"`
	Records []rawAtProtoEntry `json:"records"`
}

type rawAtProtoEntry struct {
	URI   string          `json:"uri"`
	CID   string          `json:"cid"`
	Value json.RawMessage `json:"value"`
}

type rawPlayValue struct {
	TrackName       string          `json:"trackName"`
	ArtistName      string          `json:"artistName"`
	ArtistNames     []string        `json:"artistNames"`
	Artists         []rawArtistItem `json:"artists"`
	ReleaseName     string          `json:"releaseName"`
	PlayedTime      string          `json:"playedTime"`
	CreatedAt       string          `json:"createdAt"`
	OriginURL       string          `json:"originUrl"`
	OriginURI       string          `json:"originUri"`
	MusicServiceURI string          `json:"musicServiceUri"`
}

type rawArtistItem struct {
	ArtistName string `json:"artistName"`
	Name       string `json:"name"`
}

// FetchPlays fetches all teal.fm play records for `did` from `pdsURL` into a slice.
// If `hasExisting` is non-nil, paging stops for a collection once an already-stored
// record is encountered (incremental sync).
func (c *TealClient) FetchPlays(
	ctx context.Context,
	pdsURL string,
	did string,
	hasExisting func(collection, rkey string) (bool, error),
) ([]process.PlayRecord, error) {
	var all []process.PlayRecord
	err := c.FetchPlaysStream(ctx, pdsURL, did, hasExisting, func(batch []process.PlayRecord) error {
		all = append(all, batch...)
		return nil
	})
	return all, err
}

// FetchPlaysStream fetches teal.fm play records page-by-page and invokes onPage
// for each non-empty batch so callers can persist records incrementally with low memory.
func (c *TealClient) FetchPlaysStream(
	ctx context.Context,
	pdsURL string,
	did string,
	hasExisting func(collection, rkey string) (bool, error),
	onPage func(batch []process.PlayRecord) error,
) error {
	base := strings.TrimRight(pdsURL, "/")
	if base == "" {
		return fmt.Errorf("empty PDS URL for %s", did)
	}

	for _, collection := range TealCollections {
		if err := c.fetchCollection(ctx, base, did, collection, hasExisting, onPage); err != nil {
			return fmt.Errorf("fetch %s: %w", collection, err)
		}
	}
	return nil
}

func (c *TealClient) fetchCollection(
	ctx context.Context,
	pdsBase string,
	did string,
	collection string,
	hasExisting func(collection, rkey string) (bool, error),
	onPage func(batch []process.PlayRecord) error,
) error {
	cursor := ""

	for page := 0; c.MaxPages <= 0 || page < c.MaxPages; page++ {
		q := url.Values{}
		q.Set("repo", did)
		q.Set("collection", collection)
		q.Set("limit", "100")
		if cursor != "" {
			q.Set("cursor", cursor)
		}

		endpoint := fmt.Sprintf("%s/xrpc/com.atproto.repo.listRecords?%s", pdsBase, q.Encode())
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Accept", "application/json")
		if c.UserAgent != "" {
			req.Header.Set("User-Agent", c.UserAgent)
		}

		resp, err := c.HTTPClient.Do(req)
		if err != nil {
			return err
		}

		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		if readErr != nil {
			return readErr
		}

		// Some PDS implementations return 404 when a collection doesn't exist yet on a repo.
		if resp.StatusCode == http.StatusNotFound {
			break
		}
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("PDS listRecords returned HTTP %d: %s", resp.StatusCode, truncate(string(body), 200))
		}

		var parsed listRecordsResponse
		if err := json.Unmarshal(body, &parsed); err != nil {
			return fmt.Errorf("decode listRecords response: %w", err)
		}
		if len(parsed.Records) == 0 {
			break
		}

		batch := make([]process.PlayRecord, 0, len(parsed.Records))
		hitExisting := false
		for _, entry := range parsed.Records {
			rkey := extractRKey(entry.URI)
			if rkey == "" {
				continue
			}
			if hasExisting != nil {
				exists, err := hasExisting(collection, rkey)
				if err == nil && exists {
					hitExisting = true
					break
				}
			}

			rec, ok := parsePlayRecord(did, collection, rkey, entry.Value)
			if ok {
				batch = append(batch, rec)
			}
		}

		if len(batch) > 0 && onPage != nil {
			if err := onPage(batch); err != nil {
				return err
			}
		}

		if hitExisting || parsed.Cursor == "" || parsed.Cursor == cursor {
			break
		}
		cursor = parsed.Cursor
	}

	return nil
}

// ParsePlayRecord converts a raw teal.fm JSON record into a normalized PlayRecord.
func parsePlayRecord(did, collection, rkey string, raw json.RawMessage) (process.PlayRecord, bool) {
	var v rawPlayValue
	if err := json.Unmarshal(raw, &v); err != nil {
		return process.PlayRecord{}, false
	}

	ts := parseTimestamp(v.PlayedTime)
	if ts.IsZero() {
		ts = parseTimestamp(v.CreatedAt)
	}
	if ts.IsZero() {
		return process.PlayRecord{}, false
	}

	track := strings.TrimSpace(v.TrackName)
	if track == "" {
		track = "Unknown Track"
	}

	artist := resolveArtistName(v)
	origin := firstNonEmpty(v.OriginURL, v.OriginURI, v.MusicServiceURI)

	return process.PlayRecord{
		DID:         did,
		Collection:  collection,
		RKey:        rkey,
		TrackName:   track,
		ArtistName:  artist,
		ReleaseName: strings.TrimSpace(v.ReleaseName),
		PlayedAt:    ts.UTC(),
		OriginURL:   strings.TrimSpace(origin),
	}, true
}

func resolveArtistName(v rawPlayValue) string {
	if len(v.Artists) > 0 {
		names := make([]string, 0, len(v.Artists))
		for _, a := range v.Artists {
			n := strings.TrimSpace(firstNonEmpty(a.ArtistName, a.Name))
			if n != "" {
				names = append(names, n)
			}
		}
		if len(names) > 0 {
			return strings.Join(names, ", ")
		}
	}
	if len(v.ArtistNames) > 0 {
		names := make([]string, 0, len(v.ArtistNames))
		for _, a := range v.ArtistNames {
			if s := strings.TrimSpace(a); s != "" {
				names = append(names, s)
			}
		}
		if len(names) > 0 {
			return strings.Join(names, ", ")
		}
	}
	if s := strings.TrimSpace(v.ArtistName); s != "" {
		return s
	}
	return "Unknown Artist"
}

func parseTimestamp(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	formats := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05.999999999",
		"2006-01-02T15:04:05",
	}
	for _, f := range formats {
		if t, err := time.Parse(f, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

func extractRKey(atURI string) string {
	idx := strings.LastIndexByte(atURI, '/')
	if idx < 0 || idx+1 >= len(atURI) {
		return ""
	}
	return atURI[idx+1:]
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
