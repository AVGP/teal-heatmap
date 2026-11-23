package handlers

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"teal-heatmap/internal/api"
	"teal-heatmap/internal/auth"
	"teal-heatmap/internal/config"
	"teal-heatmap/internal/store"
	"teal-heatmap/internal/visualization"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestEndToEnd_MemoryStoreSyncAndDashboard(t *testing.T) {
	cfg := &config.Config{
		Addr:          "127.0.0.1:8080",
		BaseURL:       "http://127.0.0.1:8080",
		Storage:       "memory",
		SessionSecret: []byte("0123456789abcdef0123456789abcdef"),
		IsLoopback:    true,
	}
	mem := store.NewMemoryStore()
	renderer, err := visualization.NewRenderer()
	if err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}

	today := time.Now().UTC().Format("2006-01-02")
	tealClient := api.NewTealClient()
	tealClient.HTTPClient = &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			var payload map[string]any
			if r.URL.Query().Get("collection") == "fm.teal.feed.play" {
				payload = map[string]any{
					"records": []map[string]any{
						{
							"uri": "at://did:plc:alice/fm.teal.feed.play/rk1",
							"value": map[string]any{
								"trackName":   "Teal Wave",
								"artistNames": []string{"Oceanic"},
								"releaseName": "Harbor",
								"playedTime":  today + "T12:30:00Z",
							},
						},
					},
				}
			} else {
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

	srv := NewServer(cfg, mem, auth.NewClient(cfg, mem), tealClient, renderer)
	h := srv.Handler()

	// 1. Health check works with in-memory store.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 on /healthz, got %d", rec.Code)
	}

	// 2. Login page renders without any demo/test account button.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 on login page, got %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "demo-login") {
		t.Fatalf("did not expect demo-login endpoint on login page")
	}

	// 3. Create an authenticated session in MemoryStore and sync from mocked PDS.
	appSess, err := mem.CreateAppSession(context.Background(), "did:plc:alice", "oauth-sess-1", "alice.bsky.social", "https://pds.example", time.Hour)
	if err != nil {
		t.Fatalf("CreateAppSession: %v", err)
	}
	sessCookie := &http.Cookie{Name: sessionCookieName, Value: appSess.Token}

	syncReq := httptest.NewRequest(http.MethodPost, "/sync", nil)
	syncReq.AddCookie(sessCookie)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, syncReq)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 on /sync, got %d", rec.Code)
	}

	// 4. Dashboard renders inline SVG heatmap and synced track for @alice.bsky.social.
	dashReq := httptest.NewRequest(http.MethodGet, "/", nil)
	dashReq.AddCookie(sessCookie)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, dashReq)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 on dashboard, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "@alice.bsky.social") || !strings.Contains(body, `class="hm-svg"`) || !strings.Contains(body, "Teal Wave") {
		t.Fatalf("expected dashboard with @alice.bsky.social, SVG heatmap, and synced track")
	}

	// 5. Day API returns synced play for today.
	dayReq := httptest.NewRequest(http.MethodGet, "/api/day?date="+today, nil)
	dayReq.AddCookie(sessCookie)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, dayReq)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 on /api/day, got %d", rec.Code)
	}
	var dayResp struct {
		Date  string `json:"date"`
		Plays []any  `json:"plays"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &dayResp); err != nil {
		t.Fatalf("decode /api/day response: %v", err)
	}
	if dayResp.Date != today || len(dayResp.Plays) != 1 {
		t.Fatalf("expected 1 synced play for %s, got %+v", today, dayResp)
	}

	// 6. Logout clears session.
	outReq := httptest.NewRequest(http.MethodPost, "/oauth/logout", nil)
	outReq.AddCookie(sessCookie)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, outReq)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 on logout, got %d", rec.Code)
	}
}

func TestEndToEnd_SpotifyUploadMatchesTealHeatmap(t *testing.T) {
	cfg := &config.Config{
		Addr:          "127.0.0.1:8080",
		BaseURL:       "http://127.0.0.1:8080",
		Storage:       "memory",
		SessionSecret: []byte("0123456789abcdef0123456789abcdef"),
		IsLoopback:    true,
	}
	mem := store.NewMemoryStore()
	renderer, err := visualization.NewRenderer()
	if err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}

	today := time.Now().UTC().Format("2006-01-02")
	tealClient := api.NewTealClient()
	tealClient.HTTPClient = &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			var payload map[string]any
			if r.URL.Query().Get("collection") == "fm.teal.feed.play" {
				payload = map[string]any{
					"records": []map[string]any{
						{
							"uri": "at://did:plc:alice/fm.teal.feed.play/rk1",
							"value": map[string]any{
								"trackName":   "Teal Wave",
								"artistNames": []string{"Oceanic"},
								"releaseName": "Harbor",
								"playedTime":  today + "T12:30:00Z",
							},
						},
					},
				}
			} else {
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

	srv := NewServer(cfg, mem, auth.NewClient(cfg, mem), tealClient, renderer)
	h := srv.Handler()

	// 1. Sync a teal.fm session with 1 play today and capture its rendered SVG.
	tealSess, err := mem.CreateAppSession(context.Background(), "did:plc:alice", "oauth-1", "alice.bsky.social", "https://pds.example", time.Hour)
	if err != nil {
		t.Fatalf("CreateAppSession: %v", err)
	}
	tealCookie := &http.Cookie{Name: sessionCookieName, Value: tealSess.Token}
	syncReq := httptest.NewRequest(http.MethodPost, "/sync", nil)
	syncReq.AddCookie(tealCookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, syncReq)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 on /sync, got %d", rec.Code)
	}

	tealDashReq := httptest.NewRequest(http.MethodGet, "/", nil)
	tealDashReq.AddCookie(tealCookie)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, tealDashReq)
	tealSVG := extractSVG(t, rec.Body.String())

	// 2. Build a Spotify ZIP containing the exact same play (plus a skipped <30s play and a podcast).
	var zipBuf bytes.Buffer
	zw := zip.NewWriter(&zipBuf)
	zf, err := zw.Create("Spotify Extended Streaming History/Streaming_History_Audio_2026.json")
	if err != nil {
		t.Fatalf("create zip entry: %v", err)
	}
	_, _ = zf.Write([]byte(`[
		{
			"ts": "` + today + `T12:30:00Z",
			"ms_played": 215000,
			"master_metadata_track_name": "Teal Wave",
			"master_metadata_album_artist_name": "Oceanic",
			"master_metadata_album_album_name": "Harbor",
			"spotify_track_uri": "spotify:track:12345"
		},
		{
			"ts": "` + today + `T13:00:00Z",
			"ms_played": 5000,
			"master_metadata_track_name": "Short Skip",
			"master_metadata_album_artist_name": "Skipper"
		},
		{
			"ts": "` + today + `T14:00:00Z",
			"ms_played": 180000,
			"master_metadata_track_name": null,
			"episode_name": "Podcast Ep"
		}
	]`))
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}

	// 3. Upload the Spotify ZIP via POST /upload/spotify.
	var bodyBuf bytes.Buffer
	mw := multipart.NewWriter(&bodyBuf)
	fw, err := mw.CreateFormFile("spotify_zip", "my_spotify_data.zip")
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := fw.Write(zipBuf.Bytes()); err != nil {
		t.Fatalf("write zip to form: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	uploadReq := httptest.NewRequest(http.MethodPost, "/upload/spotify", &bodyBuf)
	uploadReq.Header.Set("Content-Type", mw.FormDataContentType())
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, uploadReq)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 on /upload/spotify, got %d", rec.Code)
	}

	var spCookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookieName {
			spCookie = c
		}
	}
	if spCookie == nil || spCookie.Value == "" {
		t.Fatalf("expected %s cookie after Spotify upload", sessionCookieName)
	}

	// 4. Dashboard renders identical SVG heatmap and track info for the Spotify session.
	spDashReq := httptest.NewRequest(http.MethodGet, "/", nil)
	spDashReq.AddCookie(spCookie)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, spDashReq)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 on Spotify dashboard, got %d", rec.Code)
	}
	spBody := rec.Body.String()
	if !strings.Contains(spBody, "Spotify History") || !strings.Contains(spBody, "Teal Wave") || !strings.Contains(spBody, "Oceanic") {
		t.Fatalf("expected Spotify dashboard with track and artist")
	}
	if strings.Contains(spBody, "@Spotify History") {
		t.Fatalf("did not expect @ prefix on Spotify History handle")
	}

	spSVG := extractSVG(t, spBody)
	if spSVG != tealSVG {
		t.Fatalf("expected Spotify SVG heatmap to be byte-for-byte identical to teal.fm SVG heatmap")
	}

	// 5. Day API returns the uploaded Spotify play.
	dayReq := httptest.NewRequest(http.MethodGet, "/api/day?date="+today, nil)
	dayReq.AddCookie(spCookie)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, dayReq)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 on /api/day for Spotify session, got %d", rec.Code)
	}
	var dayResp struct {
		Date  string `json:"date"`
		Plays []any  `json:"plays"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &dayResp); err != nil {
		t.Fatalf("decode /api/day response: %v", err)
	}
	if dayResp.Date != today || len(dayResp.Plays) != 1 {
		t.Fatalf("expected 1 Spotify play for %s, got %+v", today, dayResp)
	}

	// 6. Logout cleans up the temporary Spotify session and its stored plays.
	spSess, err := mem.GetAppSession(context.Background(), spCookie.Value)
	if err != nil {
		t.Fatalf("GetAppSession: %v", err)
	}
	outReq := httptest.NewRequest(http.MethodPost, "/oauth/logout", nil)
	outReq.AddCookie(spCookie)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, outReq)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 on logout, got %d", rec.Code)
	}
	years, err := mem.GetAvailableYears(context.Background(), spSess.DID)
	if err != nil {
		t.Fatalf("GetAvailableYears: %v", err)
	}
	if len(years) != 0 {
		t.Fatalf("expected Spotify plays to be deleted on logout, still found years %v", years)
	}
}

func extractSVG(t *testing.T, html string) string {
	t.Helper()
	start := strings.Index(html, `<svg class="hm-svg"`)
	if start < 0 {
		t.Fatalf("heatmap SVG not found in HTML")
	}
	end := strings.Index(html[start:], `</svg>`)
	if end < 0 {
		t.Fatalf("closing </svg> not found in HTML")
	}
	return html[start : start+end+len(`</svg>`)]
}

func TestRealSpotifyZipIfProvided(t *testing.T) {
	zipPath := os.Getenv("SPOTIFY_TEST_ZIP")
	if zipPath == "" {
		t.Skip("set SPOTIFY_TEST_ZIP to test with a real Spotify export archive")
	}
	f, err := os.Open(zipPath)
	if err != nil {
		t.Fatalf("open %s: %v", zipPath, err)
	}
	defer f.Close()

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("spotify_zip", "my_spotify_data.zip")
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := io.Copy(fw, f); err != nil {
		t.Fatalf("copy zip: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close multipart: %v", err)
	}

	cfg := &config.Config{
		Addr:          "127.0.0.1:8080",
		BaseURL:       "http://127.0.0.1:8080",
		Storage:       "memory",
		SessionSecret: []byte("0123456789abcdef0123456789abcdef"),
		IsLoopback:    true,
	}
	mem := store.NewMemoryStore()
	renderer, err := visualization.NewRenderer()
	if err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}
	srv := NewServer(cfg, mem, auth.NewClient(cfg, mem), api.NewTealClient(), renderer)
	h := srv.Handler()

	start := time.Now()
	req := httptest.NewRequest(http.MethodPost, "/upload/spotify", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	elapsed := time.Since(start)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 on /upload/spotify, got %d", rec.Code)
	}
	t.Logf("Imported real Spotify ZIP in %v", elapsed)

	dashReq := httptest.NewRequest(http.MethodGet, "/", nil)
	for _, c := range rec.Result().Cookies() {
		dashReq.AddCookie(c)
	}
	dashRec := httptest.NewRecorder()
	h.ServeHTTP(dashRec, dashReq)
	if dashRec.Code != http.StatusOK {
		t.Fatalf("expected 200 on dashboard, got %d", dashRec.Code)
	}
	html := dashRec.Body.String()
	_ = extractSVG(t, html)
	if !strings.Contains(html, "Spotify History") {
		t.Fatalf("expected Spotify History in dashboard")
	}
	t.Logf("Dashboard HTML rendered: %d bytes", len(html))
}
