package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestTealClient_FetchPlaysAndIncrementalStop(t *testing.T) {
	client := NewTealClient()
	client.HTTPClient = &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path != "/xrpc/com.atproto.repo.listRecords" {
				return &http.Response{
					StatusCode: http.StatusNotFound,
					Body:       io.NopCloser(bytes.NewBufferString("not found")),
					Header:     make(http.Header),
				}, nil
			}
			var payload map[string]any
			switch r.URL.Query().Get("collection") {
			case "fm.teal.feed.play":
				payload = map[string]any{
					"records": []map[string]any{
						{
							"uri": "at://did:plc:abc/fm.teal.feed.play/rkey-new",
							"value": map[string]any{
								"trackName":   "Teal Skies",
								"artistNames": []string{"Oceanic", "Synthwave"},
								"releaseName": "Deep Waters",
								"playedTime":  "2026-11-22T20:15:00Z",
							},
						},
						{
							"uri": "at://did:plc:abc/fm.teal.feed.play/rkey-old",
							"value": map[string]any{
								"trackName":  "Older Track",
								"artistName": "Classic Artist",
								"playedTime": "2026-11-21T10:00:00Z",
							},
						},
					},
				}
			case "fm.teal.alpha.feed.play":
				payload = map[string]any{
					"records": []map[string]any{
						{
							"uri": "at://did:plc:abc/fm.teal.alpha.feed.play/alpha-1",
							"value": map[string]any{
								"trackName": "Alpha Track",
								"artists":   []map[string]string{{"artistName": "Alpha Artist"}},
								"createdAt": "2025-08-01T14:30:00Z",
							},
						},
					},
				}
			default:
				payload = map[string]any{"records": []any{}}
			}

			b, _ := json.Marshal(payload)
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewReader(b)),
				Header:     http.Header{"Content-Type": []string{"application/json"}},
			}, nil
		}),
	}

	// Incremental sync stops when it sees rkey-old.
	plays, err := client.FetchPlays(context.Background(), "https://pds.example", "did:plc:abc", func(collection, rkey string) (bool, error) {
		return collection == "fm.teal.feed.play" && rkey == "rkey-old", nil
	})
	if err != nil {
		t.Fatalf("FetchPlays failed: %v", err)
	}
	if len(plays) != 2 {
		t.Fatalf("expected 2 plays (rkey-new + alpha-1), got %d: %+v", len(plays), plays)
	}
	if plays[0].TrackName != "Teal Skies" || plays[0].ArtistName != "Oceanic, Synthwave" {
		t.Fatalf("unexpected first play: %+v", plays[0])
	}
	if plays[1].TrackName != "Alpha Track" || plays[1].ArtistName != "Alpha Artist" {
		t.Fatalf("unexpected second play: %+v", plays[1])
	}
}
